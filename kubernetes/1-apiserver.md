apiserver
===========
## 从 Kubernetes 的第一次 commit 开始

```bash
$ git clone -b release-0.4 https://github.com/kubernetes/kubernetes.git
$ cd kubernetes
$ git reset --hard 2c4b3a562ce34cddc3f8218a2c4d11c7310e6d56
```


## 从 cmd 开始

```golang
var (
	port                        = flag.Uint("port", 8080, "The port to listen on.  Default 8080.")
	address                     = flag.String("address", "127.0.0.1", "The address on the local server to listen to. Default 127.0.0.1")
	apiPrefix                   = flag.String("api_prefix", "/api/v1beta1", "The prefix for API requests on the server. Default '/api/v1beta1'")
	etcdServerList, machineList util.StringList
)

func init() {
	flag.Var(&etcdServerList, "etcd_servers", "Servers for the etcd (http://ip:port), comma separated")
	flag.Var(&machineList, "machines", "List of machines to schedule onto, comma separated.")
}
```
在**apiserver.go**的开始( kubernetes/cmd/apiserver/apiserver.go )，apiserver.go 首先定义了 flag，其中，etcdServerList，machineList 都实现了 flag 的接口( string 与 set )，所以可以当作 flag。

```golang
func main() {
	flag.Parse()

	if len(machineList) == 0 {
		log.Fatal("No machines specified!")
	}

	// Registry:对存储的抽象
	var (
		taskRegistry       registry.TaskRegistry
		controllerRegistry registry.ControllerRegistry
		serviceRegistry    registry.ServiceRegistry
	)
    // 从下可以看到
    // 当 len(etcdServerList) > 0
    // etcdClient 作为参数传入 Registry(taskRegistry controllerRegistry serviceRegistry)的构造
    // 否则，则使用内存作为存储 
    // 然后又将它们传入给 TaskRegistryStorage ControllerRegistryStorage ServiceRegistryStorage 
	if len(etcdServerList) > 0 {
		log.Printf("Creating etcd client pointing to %v", etcdServerList)
		etcdClient := etcd.NewClient(etcdServerList)
		taskRegistry = registry.MakeEtcdRegistry(etcdClient, machineList)
		controllerRegistry = registry.MakeEtcdRegistry(etcdClient, machineList)
		serviceRegistry = registry.MakeEtcdRegistry(etcdClient, machineList)
	} else {
		// 如果没有etcd，那么使用内存作为存储
		taskRegistry = registry.MakeMemoryRegistry()
		controllerRegistry = registry.MakeMemoryRegistry()
		serviceRegistry = registry.MakeMemoryRegistry()
	}

	containerInfo := &kube_client.HTTPContainerInfo{
		Client: http.DefaultClient,
		Port:   10250,
	}
    // 定义了不同资源的处理函数
    // 抽象工厂模式
    // 从上文中可以看到，Registry可能是etcd，也可能是内存
    // 这样 Storage (TaskRegistryStorage ControllerRegistryStorage ServiceRegistryStorage)
    // 就解耦了存储的具体依赖
	storage := map[string]apiserver.RESTStorage{
		"tasks":                  registry.MakeTaskRegistryStorage(taskRegistry, containerInfo, registry.MakeFirstFitScheduler(machineList, taskRegistry)),
		"replicationControllers": registry.MakeControllerRegistryStorage(controllerRegistry),
		"services":               registry.MakeServiceRegistryStorage(serviceRegistry),
	}

	endpoints := registry.MakeEndpointController(serviceRegistry, taskRegistry)
    // 每隔10秒，同步service的endpoint
	go util.Forever(func() { endpoints.SyncServiceEndpoints() }, time.Second*10)

	s := &http.Server{
		Addr:           fmt.Sprintf("%s:%d", *address, *port),
		Handler:        apiserver.New(storage, *apiPrefix),
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	log.Fatal(s.ListenAndServe())
}

```
main 函数主要做的是 flag 的解析，**registry** 的生成，和定义了不同资源的处理函数 **storage**，然后启动服务器。

## 服务器中的handler
```golang
// HTTP Handler interface
func (server *ApiServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	log.Printf("%s %s", req.Method, req.RequestURI)
	url, err := url.ParseRequestURI(req.RequestURI)
	if err != nil {
		server.error(err, w)
		return
	}
	if url.Path == "/index.html" || url.Path == "/" || url.Path == "" {
		server.handleIndex(w)
		return
	}
	if !strings.HasPrefix(url.Path, server.prefix) {
		server.notFound(req, w)
		return
	}
	requestParts := strings.Split(url.Path[len(server.prefix):], "/")[1:]
	if len(requestParts) < 1 {
		server.notFound(req, w)
		return
	}
	// 对哪个资源进行查询，由map导出
	storage := server.storage[requestParts[0]]
	if storage == nil {
		server.notFound(req, w)
		return
	} else {
		server.handleREST(requestParts, url, req, w, storage)
	}
}
```
上述代码位于kubernetes/pkg/apiserver/api_server.go，主要逻辑是**server.handleREST(requestParts, url, req, w, storage)**，用于处理不同资源。

```golang
func (server *ApiServer) handleREST(parts []string, url *url.URL, req *http.Request, w http.ResponseWriter, storage RESTStorage) {
	switch req.Method {
	case "GET":
		switch len(parts) {
		case 1:
			controllers, err := storage.List(url)
			if err != nil {
				server.error(err, w)
				return
			}
			server.write(200, controllers, w)
		case 2:
			task, err := storage.Get(parts[1])
			if err != nil {
				server.error(err, w)
				return
			}
			if task == nil {
				server.notFound(req, w)
				return
			}
			server.write(200, task, w)
		default:
			server.notFound(req, w)
		}
		return
	case "POST":
		if len(parts) != 1 {
			server.notFound(req, w)
			return
		}
		body, err := server.readBody(req)
		if err != nil {
			server.error(err, w)
			return
		}
		obj, err := storage.Extract(body)
		if err != nil {
			server.error(err, w)
			return
		}
		storage.Create(obj)
		server.write(200, obj, w)
		return
	case "DELETE":
		if len(parts) != 2 {
			server.notFound(req, w)
			return
		}
		err := storage.Delete(parts[1])
		if err != nil {
			server.error(err, w)
			return
		}
		server.write(200, Status{success: true}, w)
		return
	case "PUT":
		if len(parts) != 2 {
			server.notFound(req, w)
			return
		}
		body, err := server.readBody(req)
		if err != nil {
			server.error(err, w)
		}
		obj, err := storage.Extract(body)
		if err != nil {
			server.error(err, w)
			return
		}
		err = storage.Update(obj)
		if err != nil {
			server.error(err, w)
			return
		}
		server.write(200, obj, w)
		return
	default:
		server.notFound(req, w)
	}
}
```
handleREST 将根据不同参数调用不同函数，我们已经知道，storage可能是 tasks、Controllers、services，下面，我们将以tasks为例，详细介绍其GET PUT DELETE 和 PUT。

## tasks中的抽象工厂模式

### tasks的接口

```golang
/// ControllerRegistry is an interface for things that know how to store Controllers
type ControllerRegistry interface {
	ListControllers() ([]api.ReplicationController, error)
	GetController(controllerId string) (*api.ReplicationController, error)
	CreateController(controller api.ReplicationController) error
	UpdateController(controller api.ReplicationController) error
	DeleteController(controllerId string) error
}
```
etcd_registry 和 memory_registry 都实现了 ControllerRegistry 接口

### 接口的实现

```golang
// Implementation of RESTStorage for the api server.
type ControllerRegistryStorage struct {
	registry ControllerRegistry
}
```
所以可以将其构造对象传进来，用以解耦Storage跟存储的具体实现。

```golang
// ControllerRegistryStorage 内嵌了 ControllerRegistry
type ControllerRegistryStorage struct {
	registry ControllerRegistry
}

func MakeControllerRegistryStorage(registry ControllerRegistry) apiserver.RESTStorage {
	return &ControllerRegistryStorage{
		registry: registry,
	}
}

func (storage *ControllerRegistryStorage) List(*url.URL) (interface{}, error) {
	var result ReplicationControllerList
	controllers, err := storage.registry.ListControllers()
	if err == nil {
		result = ReplicationControllerList{
			Items: controllers,
		}
	}
	return result, err
}

func (storage *ControllerRegistryStorage) Get(id string) (interface{}, error) {
	return storage.registry.GetController(id)
}

func (storage *ControllerRegistryStorage) Delete(id string) error {
	return storage.registry.DeleteController(id)
}

func (storage *ControllerRegistryStorage) Extract(body string) (interface{}, error) {
	result := ReplicationController{}
	err := json.Unmarshal([]byte(body), &result)
	return result, err
}

func (storage *ControllerRegistryStorage) Create(controller interface{}) error {
	return storage.registry.CreateController(controller.(ReplicationController))
}

func (storage *ControllerRegistryStorage) Update(controller interface{}) error {
	return storage.registry.UpdateController(controller.(ReplicationController))
}
```
可以看到，ControllerRegistryStorage 结构体内嵌了 ControllerRegistry，ControllerRegistryStorage的List、
Get、Delete等方法，其实都是调用Registry的方法，也就是etcd_registry或者memory_registry的方法。