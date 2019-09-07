// +build ignore

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	"seehuhn.de/go/websocket"
)

var (
	port    = flag.String("port", "8080", "what TCP port to bind to")
	scratch = flag.String("dir", "scratch", "directory for wstest to work in")
)

type testList []string

func (tl *testList) String() string {
	return strings.Join(*tl, ",")
}

func (tl *testList) Set(value string) error {
	for _, s := range strings.Split(value, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			*tl = append(*tl, s)
		}
	}
	return nil
}

var testCases testList

func runDocker(scratch string, done chan<- struct{}) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		log.Fatal("cannot find docker: ", err)
	}

	scratch, err = filepath.Abs(scratch)
	if err != nil {
		log.Fatal("cannot find scratch: ", err)
	}

	dockerArgs := []string{
		"docker",
		"run",
		"--rm",
		"-v", scratch + ":/scratch",
		"--name", "fuzzingclient",
		"--net", "host",
		"crossbario/autobahn-testsuite",

		"/usr/local/bin/wstest",
		"-m", "fuzzingclient",
		"-s", "/scratch/spec.json",
	}
	log.Println("starting docker")
	log.Println("========================================")
	procAttr := &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}
	process, err := os.StartProcess(dockerPath, dockerArgs, procAttr)
	if err != nil {
		log.Fatal("cannot start docker: ", err)
	}
	state, err := process.Wait()
	log.Println("========================================")
	if err != nil {
		log.Printf("DOCKER FAILED: status=%d, error=%s\n", state, err.Error())
	} else {
		log.Println("docker terminated")
	}

	close(done)
}

var wg sync.WaitGroup

func echo(conn *websocket.Conn) {
	wg.Add(1)
	defer wg.Done()

	defer conn.Close(websocket.StatusOK, "")

	for {
		tp, r, err := conn.ReceiveMessage()
		if err == websocket.ErrConnClosed {
			break
		} else if err != nil {
			log.Println("read error", err)
			break
		}

		w, err := conn.SendMessage(tp)
		if err != nil {
			log.Println("write error", err)
			n, err := io.Copy(ioutil.Discard, r)
			log.Println("discard", n, err)
			break
		}

		n, err := io.Copy(w, r)
		if err != nil {
			log.Println("ECHO", n, err)
			io.Copy(ioutil.Discard, r)
		}
		err = w.Close()
		if err != nil && err != websocket.ErrConnClosed {
			log.Println("CLOSE ERROR", err)
		}
	}
}

func main() {
	flag.Var(&testCases, "test", "comma-separated list of tests to perform")
	flag.Parse()

	// find an interface which can be reached from inside the Docker container
	ifaces, err := net.Interfaces()
	if err != nil {
		log.Fatal(err)
	}
	var useAddr net.IP
ifaceLoop:
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if ok && ipnet.IP.IsGlobalUnicast() {
				useAddr = ipnet.IP
				break ifaceLoop
			}
		}
	}
	if useAddr == nil {
		log.Fatal("no usable IP address found")
	}
	listenAddr := net.JoinHostPort(useAddr.String(), *port)
	log.Println("listening at", listenAddr)

	// extract the list of tests
	if len(testCases) == 0 {
		testCases = []string{"*"}
	}
	b, err := json.Marshal(testCases)
	if err != nil {
		log.Fatal(err)
	}
	cases := string(b)

	// Create the configuration file
	os.Mkdir(*scratch, 0755)
	tmpl, err := template.ParseFiles("spec.tmpl")
	if err != nil {
		log.Fatal(err)
	}
	spec, err := os.Create(filepath.Join(*scratch, "spec.json"))
	if err != nil {
		log.Fatal(err)
	}
	err = tmpl.Execute(spec, map[string]string{
		"host":  listenAddr,
		"cases": cases,
	})
	if err != nil {
		log.Fatal(err)
	}

	// open the socket
	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatal(err)
	}

	// start the Docker container
	dockerDone := make(chan struct{})
	go runDocker(*scratch, dockerDone)

	// start the websocket server
	serverDone := make(chan struct{})
	go func() {
		websocket := &websocket.Handler{
			Handle:     echo,
			ServerName: "FishyBunny",
		}
		http.Serve(l, websocket)
		log.Println("server terminated")
		close(serverDone)
	}()

	<-dockerDone
	l.Close()
	<-serverDone

	fmt.Printf("\nThe report is in %q.\n",
		filepath.Join(*scratch, "index.html"))

	fmt.Println("\nif the server hangs here, some clients have not terminated")
	wg.Wait()

}
