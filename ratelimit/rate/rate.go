// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package rate provides a rate limiter.
package rate

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// 定义每秒产生令牌的速率
type Limit float64

// Inf is the infinite rate limit; it allows all events (even if burst is zero).
const Inf = Limit(math.MaxFloat64)

// 将间隔转换成速率
func Every(interval time.Duration) Limit {
	if interval <= 0 {
		return Inf
	}
	return 1 / Limit(interval.Seconds())
}

type Limiter struct {
	mu        sync.Mutex
	limit     Limit     // 表示一秒内生成token的速率
	burst     int       // 桶的大小
	tokens    float64   // 现在桶内有多少token
	last      time.Time // 上一次令牌数目变化的时间
	lastEvent time.Time // 上一次限流事件发生的时间
}

// Limit returns the maximum overall event rate.
func (lim *Limiter) Limit() Limit {
	lim.mu.Lock()
	defer lim.mu.Unlock()
	return lim.limit
}

// 木桶大小，表示支持的瞬间并发量
// 除非limit为Inf，否则木桶大小为0，表示不允许访问
func (lim *Limiter) Burst() int {
	lim.mu.Lock()
	defer lim.mu.Unlock()
	return lim.burst
}

func NewLimiter(r Limit, b int) *Limiter {
	return &Limiter{
		limit: r,
		burst: b,
	}
}

func (lim *Limiter) Allow() bool {
	return lim.AllowN(time.Now(), 1)
}

func (lim *Limiter) AllowN(now time.Time, n int) bool {
	return lim.reserveN(now, n, 0).ok
}

// A Reservation holds information about events that are permitted by a Limiter to happen after a delay.
// A Reservation may be canceled, which may enable the Limiter to permit additional events.
type Reservation struct {
	ok        bool // 申请令牌是否成功
	lim       *Limiter
	tokens    int // 申请的tokens数目
	timeToAct time.Time
	limit     Limit // 申请令牌时API限流器生成token的速率
}

// OK returns whether the limiter can provide the requested number of tokens
// within the maximum wait time.  If OK is false, Delay returns InfDuration, and
// Cancel does nothing.
func (r *Reservation) OK() bool {
	return r.ok
}

// Delay is shorthand for DelayFrom(time.Now()).
func (r *Reservation) Delay() time.Duration {
	return r.DelayFrom(time.Now())
}

// InfDuration is the duration returned by Delay when a Reservation is not OK.
const InfDuration = time.Duration(1<<63 - 1)

// DelayFrom returns the duration for which the reservation holder must wait
// before taking the reserved action.  Zero duration means act immediately.
// InfDuration means the limiter cannot grant the tokens requested in this
// Reservation within the maximum wait time.
func (r *Reservation) DelayFrom(now time.Time) time.Duration {
	if !r.ok {
		return InfDuration
	}
	// 在reserveN函数中，更新了timeToAct字段，该字段表示成功拿到令牌的时间
	// 减去now，表示再过delay，就能成功拿到令牌
	delay := r.timeToAct.Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}

// Cancel is shorthand for CancelAt(time.Now()).
func (r *Reservation) Cancel() {
	r.CancelAt(time.Now())
}

// CancelAt indicates that the reservation holder will not perform the reserved action
// and reverses the effects of this Reservation on the rate limit as much as possible,
// considering that other reservations may have already been made.
func (r *Reservation) CancelAt(now time.Time) {
	if !r.ok {
		return
	}

	r.lim.mu.Lock()
	defer r.lim.mu.Unlock()
	// 如果限流器生成token的速率为无穷 或者 木桶里面没有token 或者 timeToAct 的时间已经过期
	// 直接返回，因此此时令牌预约结构体已经没有意义，或者说已经被取消了
	if r.lim.limit == Inf || r.tokens == 0 || r.timeToAct.Before(now) {
		return
	}
	// 因为要取消令牌预约结构体，所以我们要将申请的令牌还给限流器
	// 上一次申请令牌的时间

	// 首先，算出要归还的令牌数目
	// The duration between lim.lastEvent and r.timeToAct tells us how many tokens were reserved
	// after r was obtained. These tokens should not be restored.
	// r.limt.lastEvent ： 最近一次timeToAct的时间
	// r.timeToAct ： 满足令牌消费的时刻，即 消费时刻 + 等待时长
	// 比如有两个令牌预约结构体，一个的timeToAct是10:00 另一个是10:20，那么r.lim.lastEvent的时间就是10:20
	// 不过，我仍然不知道为什么归还的令牌数目要减去r.limit.tokensFromDuration(r.lim.lastEvent.Sub(r.timeToAct))
	// 我无法理解...
	restoreTokens := float64(r.tokens) - r.limit.tokensFromDuration(r.lim.lastEvent.Sub(r.timeToAct))
	if restoreTokens <= 0 {
		return
	}
	now, _, tokens := r.lim.advance(now)
	tokens += restoreTokens
	if burst := float64(r.lim.burst); tokens > burst {
		tokens = burst
	}
	// 更新状态
	r.lim.last = now
	r.lim.tokens = tokens
	if r.timeToAct == r.lim.lastEvent {
		prevEvent := r.timeToAct.Add(r.limit.durationFromTokens(float64(-r.tokens)))
		if !prevEvent.Before(now) {
			r.lim.lastEvent = prevEvent
		}
	}
}

func (lim *Limiter) Reserve() *Reservation {
	return lim.ReserveN(time.Now(), 1)
}

func (lim *Limiter) ReserveN(now time.Time, n int) *Reservation {
	r := lim.reserveN(now, n, InfDuration)
	return &r
}

// Wait is shorthand for WaitN(ctx, 1).
func (lim *Limiter) Wait(ctx context.Context) (err error) {
	return lim.WaitN(ctx, 1)
}

func (lim *Limiter) WaitN(ctx context.Context, n int) (err error) {
	lim.mu.Lock()
	burst := lim.burst // 木桶的大小
	limit := lim.limit // 生成令牌的速率
	lim.mu.Unlock()

	if n > burst && limit != Inf {
		return fmt.Errorf("rate: Wait(n=%d) exceeds limiter's burst %d", n, burst)
	}
	// 如果context已经被cancel掉，那么返回error
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	// Determine wait limit
	now := time.Now()
	waitLimit := InfDuration
	if deadline, ok := ctx.Deadline(); ok {
		// 再经过waitLimit时间，context就会到deadline
		// i.e waitLimit就是申请令牌的最大容忍时间
		waitLimit = deadline.Sub(now)
	}
	// 调用reserveN，申请N个令牌
	r := lim.reserveN(now, n, waitLimit)
	if !r.ok {
		return fmt.Errorf("rate: Wait(n=%d) would exceed context deadline", n)
	}
	// 再过delay时间，就能拿到令牌
	delay := r.DelayFrom(now)
	if delay == 0 {
		return nil
	}
	// 申请一个timer，等待delay时间，返回
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		// 时间到，返回
		return nil
	case <-ctx.Done():
		// delay时间还未到，context就已经被done了，返回error
		r.Cancel() // 取消令牌申请结构体，归还申请的token
		return ctx.Err()
	}
}

// SetLimit is shorthand for SetLimitAt(time.Now(), newLimit).
func (lim *Limiter) SetLimit(newLimit Limit) {
	lim.SetLimitAt(time.Now(), newLimit)
}

// 设置限流器产生令牌的速率
func (lim *Limiter) SetLimitAt(now time.Time, newLimit Limit) {
	lim.mu.Lock()
	defer lim.mu.Unlock()

	now, _, tokens := lim.advance(now)

	lim.last = now // 木桶大小变化时，lim.last会发生变化
	lim.tokens = tokens
	lim.limit = newLimit
}

func (lim *Limiter) SetBurst(newBurst int) {
	lim.SetBurstAt(time.Now(), newBurst)
}

// SetBurstAt 为限流器设置新的木桶大小
func (lim *Limiter) SetBurstAt(now time.Time, newBurst int) {
	lim.mu.Lock()
	defer lim.mu.Unlock()

	now, _, tokens := lim.advance(now)

	lim.last = now
	lim.tokens = tokens
	lim.burst = newBurst
}

// now 表示申请令牌时的时间
// n 表示申请令牌的个数
// maxFutureReserve 表示申请最长等待事件，因此申请时可能木桶里面的令牌还不够，所以可能需要等待
func (lim *Limiter) reserveN(now time.Time, n int, maxFutureReserve time.Duration) Reservation {
	lim.mu.Lock()
	defer lim.mu.Unlock()
	// 如果限流器生成令牌的速率为无穷
	// 那么对于任意令牌的申请，都能满足
	if lim.limit == Inf {
		return Reservation{
			ok:        true,
			lim:       lim,
			tokens:    n,
			timeToAct: now,
		}
	} else if lim.limit == 0 {
		// 如果限流器生成令牌的速率为0
		// 那么限流器能否满足令牌的申请，只能看木桶剩下的令牌了
		var ok bool
		if lim.burst >= n {
			ok = true
			lim.burst -= n
		}
		return Reservation{
			ok:        ok,
			lim:       lim,
			tokens:    lim.burst,
			timeToAct: now,
		}
	}
	// now : 现在的事件
	// last : 上次拿token的事件
	// tokens : 从上次到现在，限流器生成的token数目与限流器本身木桶里token数目之和
	// 注意： 由于限流器生成token的速率一定，因此，我们可以通过时间差，算出这段时间生成的token数目
	now, last, tokens := lim.advance(now)

	// 减去请求的token数目，如果tokens小于0，说明需要等待，算出等待时间
	tokens -= float64(n)
	var waitDuration time.Duration
	if tokens < 0 {
		waitDuration = lim.limit.durationFromTokens(-tokens)
	}

	// 请求是否能申请成功，由木桶里面桶大小和用户所能容忍的最长申请时间决定
	ok := n <= lim.burst && waitDuration <= maxFutureReserve
	// 填充Reservation结构体，用以返回
	r := Reservation{
		ok:    ok,
		lim:   lim,
		limit: lim.limit,
	}
	if ok {
		r.tokens = n
		r.timeToAct = now.Add(waitDuration)
	}

	// 更新限流器状态
	if ok {
		lim.last = now
		lim.tokens = tokens
		lim.lastEvent = r.timeToAct
	} else {
		lim.last = last
	}

	return r
}

// advance calculates and returns an updated state for lim resulting from the passage of time.
// lim is not changed.
// advance requires that lim.mu is held.
func (lim *Limiter) advance(now time.Time) (newNow time.Time, newLast time.Time, newTokens float64) {
	last := lim.last
	if now.Before(last) {
		last = now
	}

	// Calculate the new number of tokens, due to time that passed.
	// elapsed 表示与上次更新令牌时的时间差
	elapsed := now.Sub(last)
	// 通过时间差，算出这段时间内产生了多少令牌
	delta := lim.limit.tokensFromDuration(elapsed)
	tokens := lim.tokens + delta
	if burst := float64(lim.burst); tokens > burst {
		tokens = burst
	}
	return now, last, tokens
}

// 返回产生tokens个令牌，需要的时间
func (limit Limit) durationFromTokens(tokens float64) time.Duration {
	if limit <= 0 {
		return InfDuration
	}
	seconds := tokens / float64(limit)
	return time.Duration(float64(time.Second) * seconds)
}

// 将时间差转换成这段时间产生的令牌数
func (limit Limit) tokensFromDuration(d time.Duration) float64 {
	if limit <= 0 {
		return 0
	}
	return d.Seconds() * float64(limit)
}
