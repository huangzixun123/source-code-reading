Go Time 源码阅读
============================================
## Go Time

Go Time 是 Golang 官方的 API 限流器，本仓库主要记录阅读该API限流器源码的的笔记，同时对相应的代码标上注释。

## 可能对其他人有帮助的文档

1. [Golang 标准库限流器 time/rate 使用介绍](https://www.cyhone.com/articles/usage-of-golang-rate/)
2. [Golang 标准库限流器 time/rate 实现剖析 ](https://www.cyhone.com/articles/analisys-of-golang-rate/)
3. [golang/time/rate官方仓库](https://github.com/golang/time)

## 注意
1. [注释版的仓库](rate/rate.go)已经删除与源码阅读不相关的文件
2. 在Golang的新版本中，time.Duration类型变成了float64，而不再是int64，因此，[Golang 标准库限流器 time/rate 实现剖析](https://www.cyhone.com/articles/analisys-of-golang-rate/)中部分函数的解释，已经过时了。但是，对于float64与不同类型乘除的处理，如何避免精度损失从而得到最精确的精度，其解决方法仍然有很大的参考意义。因此，很值得一看。