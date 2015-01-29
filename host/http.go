package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/julienschmidt/httprouter"
	"github.com/flynn/flynn/Godeps/_workspace/src/golang.org/x/net/context"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/sse"
)

type Host struct {
	state   *State
	backend Backend
}

func (h *Host) StopJob(id string) error {
	job := h.state.GetJob(id)
	if job == nil {
		return errors.New("host: unknown job")
	}
	switch job.Status {
	case host.StatusStarting:
		h.state.SetForceStop(id)
		return nil
	case host.StatusRunning:
		return h.backend.Stop(id)
	default:
		return errors.New("host: job is already stopped")
	}
}

func (h *Host) streamEvents(id string, w http.ResponseWriter) error {
	ch := h.state.AddListener(id)
	go func() {
		<-w.(http.CloseNotifier).CloseNotify()
		h.state.RemoveListener(id, ch)
	}()
	enc := json.NewEncoder(sse.NewWriter(w))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(200)
	w.(http.Flusher).Flush()
	for data := range ch {
		if err := enc.Encode(data); err != nil {
			return err
		}
		w.(http.Flusher).Flush()
	}
	return nil
}

type jobAPI struct {
	host *Host
}

func (h *jobAPI) ListJobs(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		if err := h.host.streamEvents("all", w); err != nil {
			httphelper.Error(w, err)
		}
		return
	}
	res := h.host.state.Get()

	httphelper.JSON(w, 200, res)
}

func (h *jobAPI) GetJob(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id := httphelper.ParamsFromContext(ctx).ByName("id")

	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		if err := h.host.streamEvents(id, w); err != nil {
			httphelper.Error(w, err)
		}
		return
	}
	job := h.host.state.GetJob(id)
	httphelper.JSON(w, 200, job)
}

func (h *jobAPI) StopJob(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id := httphelper.ParamsFromContext(ctx).ByName("id")
	if err := h.host.StopJob(id); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *jobAPI) RegisterRoutes(r *httprouter.Router) error {
	r.GET("/host/jobs", httphelper.WrapHandler(h.ListJobs))
	r.GET("/host/jobs/:id", httphelper.WrapHandler(h.GetJob))
	r.DELETE("/host/jobs/:id", httphelper.WrapHandler(h.StopJob))
	return nil
}

func serveHTTP(host *Host, attach *attachHandler) (*httprouter.Router, error) {
	l, err := net.Listen("tcp", ":1113")
	if err != nil {
		return nil, err
	}

	r := httprouter.New()

	r.POST("/attach", attach.ServeHTTP)

	jobAPI := &jobAPI{host}
	jobAPI.RegisterRoutes(r)

	go http.Serve(l, r)

	return r, nil
}
