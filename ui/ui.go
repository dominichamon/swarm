// Package ui defines a UI for visualizing a hive.
package main

import (
	"flag"
	"fmt"
	"html"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/dominichamon/hive"
	"github.com/golang/glog"
	"golang.org/x/net/context"

	pb "github.com/dominichamon/hive/proto"
)

var (
	port = flag.Int("port", 1248, "The port on which to listen for HTTP")

	addr  = flag.String("addr", "", "The multicast address to use for discovery")
	dport = flag.Int("dport", 9997, "The port on which to listen for discovery")

	worker workerMap
	status map[string]*pb.StatusResponse
)

type workerMap struct {
	sync.RWMutex
	worker map[string]*hive.Worker
}

func (m *workerMap) add(s *hive.Worker) {
	m.Lock()
	m.worker[s.Id] = s
	m.Unlock()
}

func (m *workerMap) remove(s *hive.Worker) error {
	m.RLock()
	defer m.RUnlock()
	if _, ok := m.worker[s.Id]; !ok {
		return fmt.Errorf("worker %q not found", s.Id)
	}

	m.Lock()
	defer m.Unlock()
	if _, ok := m.worker[s.Id]; !ok {
		return fmt.Errorf("worker %q not found", s.Id)
	}
	delete(m.worker, s.Id)

	return nil
}

func init() {
	worker.Lock()
	worker.worker = make(map[string]*hive.Worker)
	worker.Unlock()

	status = make(map[string]*pb.StatusResponse)
}

func handleError(w http.ResponseWriter, code int, err error) {
	w.WriteHeader(code)
	fmt.Fprintf(w, "%q", html.EscapeString(err.Error()))
	glog.Error(err)
}

func Index(w http.ResponseWriter, req *http.Request) {
	t, err := template.New("index").Parse(
		`<html><body>
		<table>
		<thead><th>Id</th><th>IP</th><th>Host</th><th>Total RAM</th><th>Free RAM</th></thead>
		{{range $id, $status := .}}
			<tr>
				<td>{{$id}}</td>
				<td>{{$status.Ip}}</td>
				<td>{{$status.Hostname}}</td>
				<td>{{$status.TotalRam}}</td>
				<td>{{$status.FreeRam}}</td>
			</tr>
		{{end}}
		</table>
		</body></html>`)
	if err != nil {
		handleError(w, http.StatusInternalServerError, err)
		return
	}

	if err = t.Execute(w, status); err != nil {
		handleError(w, http.StatusInternalServerError, err)
		return
	}
}

func handleDiscoveryAcks(ctx context.Context, addrs <-chan string) {
	for saddr := range addrs {
		glog.Infof("Discovered worker at %s", saddr)

		host, port, err := net.SplitHostPort(saddr)
		if err != nil {
			glog.Error(err)
			continue
		}

		p, err := strconv.ParseInt(port, 10, 32)
		if err != nil {
			glog.Error(err)
			continue
		}

		s, err := hive.NewWorker(host, int(p))
		if err != nil {
			glog.Errorf("Failed to create new worker: %s", err)
			continue
		}

		glog.Infof("Connected to %+v", s)
		worker.add(s)

		stat, err := s.Client.Status(ctx, &pb.StatusRequest{})
		if err != nil {
			glog.Warning(err)
		}
		glog.Infof("Status of %s: %+v", s.Id, stat)
		// TODO: lock
		status[s.Id] = stat

		// TODO: remove old worker
	}
}

func updateStatus(ctx context.Context) {
	for {
		worker.RLock()
		ss := make([]*hive.Worker, len(worker.worker))
		i := 0
		for _, s := range worker.worker {
			ss[i] = s
			i++
		}
		worker.RUnlock()

		for _, s := range ss {
			stat, err := s.Client.Status(ctx, &pb.StatusRequest{})
			if err != nil {
				glog.Warningf("Failed to get status for %+v: %s", s, err)
				continue
			}
			glog.Infof("Status of %s: %+v", s.Id, stat)
			// TODO: lock
			status[s.Id] = stat
		}

		time.Sleep(1 * time.Minute)
	}
}

func main() {
	flag.Parse()

	ctx := context.Background()

	go func() {
		for {
			addrs := make(chan string)
			err := hive.Ping(*addr, *dport, addrs)
			if err != nil {
				glog.Error(err)
				goto sleep
			}
			handleDiscoveryAcks(ctx, addrs)
		sleep:
			time.Sleep(5 * time.Minute)
		}
	}()
	go updateStatus(ctx)

	http.HandleFunc("/", Index)
	glog.Infof("listening on port %d", *port)
	glog.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}
