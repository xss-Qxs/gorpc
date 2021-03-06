// go test -v github.com/johntech-o/gorpc
package gorpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/johntech-o/gorpc/utility/pprof"
	cal"github.com/johntech-o/gorpc/utility/calculator"
)

var client *Client

const (
	ExecGoroutines    = 1000
	ExecPerGoroutines = 1000
)

func init() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	go func() {
		log.Println(http.ListenAndServe(":6789", nil))
	}()
	time.Sleep(time.Microsecond * 2)
}

type TestABC struct {
	A, B, C string
}

type TestRpcInt struct {
	i int
}

func (r *TestRpcInt) Update(n int, res *int) error {
	r.i = n
	*res = r.i + 100
	return nil
}

var callTimes = 0

func (r *TestRpcInt) ReturnErr(n int, res *int) error {
	*res = 100
	if n == 1 {
		if callTimes == 0 {
			callTimes++
			return &Error{10000, ErrTypeCanRetry, "user defined retry error"}
		} else {
			return Error{100001, ErrTypeLogic, "after retry user logic error"}
		}
	}
	return errors.New("user defined common error")

}

const EchoContent = "hello echo struct"

func (r *TestRpcInt) EchoStruct(arg TestABC, res *string) error {
	*res = EchoContent
	return nil
}

var StopClient2 = make(chan struct{})
var MaxQps uint64

func TestStartServerClient(t *testing.T) {
	go func() {
		s := NewServer("127.0.0.1:6668")
		s.Register(new(TestRpcInt))
		s.Serve()
		panic("server fail")
	}()
	time.Sleep(time.Millisecond * 2)
	netOptions := NewNetOptions(time.Second*10, time.Second*20, time.Second*20)
	// client to ben test server
	client = NewClient(netOptions)

	// client2 to get go gorpc status
	client2 := NewClient(netOptions)
	go func() {
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-StopClient2:
				return
			case <-timer.C:
				var reply string
				var err *Error

				err = client2.CallWithAddress("127.0.0.1:6668", "RpcStatus", "CallStatus", false, &reply)
				if err != nil {
					fmt.Println("server call amount error: ", err.Error())
					continue
				}
				var qps = struct {
					Result uint64
					Errno  int
				}{}
				qpsStr := client.Qps()
				if err := json.Unmarshal([]byte(qpsStr), &qps); err != nil {
					fmt.Println(err)
				}
				if qps.Result > MaxQps {
					MaxQps = qps.Result
				}
				fmt.Println("server call status: ", reply)
				fmt.Println("client conn status: ", client.ConnsStatus())
				fmt.Println("client conn Qps   : ", qpsStr)

			default:
			}
		}
	}()
}

// common case test fault-tolerant
func TestInvalidParams(t *testing.T) {
	var up int
	t.Log("test invalid service")
	e := client.CallWithAddress("127.0.0.1:6668", "xxxx", "Update", 5, &up)
	if e.Errno() == 400 {
		t.Log("ok", e.Error())
	} else {
		t.Error("fail", e)
	}

	t.Log("test invalid method")
	e = client.CallWithAddress("127.0.0.1:6668", "TestRpcInt", "xxxx", 5, &up)
	if e.Errno() == 400 {
		t.Log("ok", e.Error())
	} else {
		t.Error("fail", e)
	}

	t.Log("test invalid args")
	e = client.CallWithAddress("127.0.0.1:6668", "TestRpcInt", "Update", "5", &up)
	if e.Errno() == 400 {
		t.Log("ok", e.Error())
	} else {
		t.Error("fail", e)
	}

	t.Log("test invalid reply")
	var upStr string
	e = client.CallWithAddress("127.0.0.1:6668", "TestRpcInt", "Update", 5, &upStr)
	if e.Errno() == 106 {
		t.Log("ok", e.Error())
	} else {
		t.Error("fail", e)
	}

	t.Log("test normal update")
	e = client.CallWithAddress("127.0.0.1:6668", "TestRpcInt", "Update", 5, &up)
	if e != nil {
		t.Error("fail", e, up)
	} else {
		if up == 105 {
			t.Log("ok", up, e)
		}
	}

	var res int
	t.Log("test remote return can retry error")
	e = client.CallWithAddress("127.0.0.1:6668", "TestRpcInt", "ReturnErr", 1, &res)
	if e.Errno() == 100001 {
		t.Log("ok", e.Error())
	} else {
		t.Error("fail", e)
	}
	t.Log("test remote return error")
	e = client.CallWithAddress("127.0.0.1:6668", "TestRpcInt", "ReturnErr", 2, &res)
	if e.Errno() == 500 {
		t.Log("ok", e.Error())
	} else {
		t.Error("fail", e)
	}
}

func TestEchoStruct(t *testing.T) {

	var results = struct {
		content map[string]int
		sync.Mutex
	}{content: make(map[string]int, 1000)}

	var counter = cal.NewCallCalculator()
	var wgCreate sync.WaitGroup
	var wgFinish sync.WaitGroup
	var startRequestCh = make(chan struct{})
	for i := 0; i < ExecGoroutines; i++ {
		wgCreate.Add(1)
		go func() {
			wgCreate.Done()
			wgFinish.Add(1)
			defer wgFinish.Done()
			<-startRequestCh
			for i := 0; i < ExecPerGoroutines; i++ {
				var res string
				id := counter.Start()
				err := client.CallWithAddress("127.0.0.1:6668", "TestRpcInt", "EchoStruct", TestABC{"aaa", "bbb", "ccc"}, &res)
				counter.End(id)
				if err != nil {
					results.Lock()
					results.content[err.Error()] += 1
					results.Unlock()
					continue
				}
				results.Lock()
				results.content[res] += 1
				results.Unlock()
			}

		}()
	}
	wgCreate.Wait()
	// pprof result
	pprof.MemStats()
	// start to send request
	close(startRequestCh)
	wgFinish.Wait()
	close(StopClient2)
	pprof.MemStats()
	pprof.StatIncrement(pprof.HeapObjects, pprof.TotalAlloc, pprof.PauseTotalMs, pprof.NumGC)

	// output rpc result
	if len(results.content) > 1 {
		t.Error("have failed call")
	}
	for result, count := range results.content {
		t.Logf("TestEchoStruct result: %s ,count: %d \n", result, count)
	}
	// client request result
	counter.Summary()
	fmt.Printf("Max Client Qps: %d \n", MaxQps)
	time.Sleep(time.Microsecond)
}
