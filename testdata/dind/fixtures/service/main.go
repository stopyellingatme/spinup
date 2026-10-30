// Command service is the sidecar service of the dind suite (ADR 0022 §2). run.sh copies it
// to services/echo/ in the generated app and builds it with the Dockerfile next to it.
//
// With no argument, it serves HTTP on $PORT:
//
//	GET /healthz  answers 200 "ok"
//	GET /info     answers {"greeting": $GREETING}
//
// The distroless image has no shell, so the binary also has the small tools that the suite
// runs with docker exec and docker run:
//
//	service healthcheck   exits 0 when http://127.0.0.1:$PORT/healthz answers 200
//	service get <url>     prints the body of a GET request, and exits 1 unless it is 200
//	service ls <dir>      prints the names in a directory, one for each line
//	service cat <file>    prints the content of a file
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 {
		if err := tool(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "service:", err)
			os.Exit(1)
		}
		return
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"greeting": os.Getenv("GREETING")})
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	fmt.Println("service: listening on :" + port)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		os.Exit(1)
	}
}

func tool(name string, args []string) error {
	switch {
	case name == "healthcheck" && len(args) == 0:
		port := os.Getenv("PORT")
		if port == "" {
			port = "8090"
		}
		_, err := get("http://127.0.0.1:" + port + "/healthz")
		return err
	case name == "get" && len(args) == 1:
		body, err := get(args[0])
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(body)
		return err
	case name == "ls" && len(args) == 1:
		entries, err := os.ReadDir(args[0])
		if err != nil {
			return err
		}
		for _, e := range entries {
			fmt.Println(e.Name())
		}
		return nil
	case name == "cat" && len(args) == 1:
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	}
	return fmt.Errorf("unknown command %q with %d arguments (use healthcheck, get <url>, ls <dir>, or cat <file>)", name, len(args))
}

func get(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("GET %s answered %s", url, resp.Status)
	}
	return body, nil
}
