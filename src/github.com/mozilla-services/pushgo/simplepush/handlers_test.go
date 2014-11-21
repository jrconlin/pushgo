package simplepush

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func newTestHandler(t *testing.T) (*Handler, *Application) {

	tlogger, _ := NewLogger(&TestLogger{DEBUG, t})

	mx := &TestMetrics{}
	mx.Init(nil, nil)
	store := &NoStore{logger: tlogger, maxChannels: 10}
	count := int32(0)
	pping := &NoopPing{}
	app := &Application{
		hostname:           "test",
		host:               "test",
		clientMinPing:      10 * time.Second,
		clientHelloTimeout: 10 * time.Second,
		clientMux:          new(sync.RWMutex),
		pushLongPongs:      true,
		tokenKey:           []byte(""),
		metrics:            mx,
		clients:            make(map[string]*Client),
		clientCount:        &count,
		store:              store,
		propping:           pping,
	}
	app.SetLogger(tlogger)
	server := &Serv{}
	server.Init(app, server.ConfigStruct())
	app.SetServer(server)
	locator := &NoLocator{logger: tlogger}
	router := NewRouter()
	router.Init(app, router.ConfigStruct())
	router.SetLocator(locator)
	app.SetRouter(router)

	handler := &Handler{
		app:        app,
		logger:     tlogger,
		store:      store,
		router:     router,
		metrics:    mx,
		tokenKey:   app.TokenKey(),
		maxDataLen: 140,
		propping:   pping,
	}
	return handler, app
}

type TFlushReply struct {
	LastAccessed int64  `json:"lastaccessed"`
	Channel      string `json:"channel"`
	Version      int64  `json:"version"`
	Data         string `json:"data"`
}

func Test_UpdateHandler(t *testing.T) {
	var err error
	uaid := "deadbeef000000000000000000000000"
	chid := "decafbad000000000000000000000000"
	data := "This is a test of the emergency broadcasting system."

	handler, app := newTestHandler(t)
	noPush := PushWS{
		Uaid:     uaid,
		deviceID: []byte(chid),
		Socket:   nil,
		Born:     time.Now(),
	}

	worker := &NoWorker{Socket: &noPush,
		Logger: app.Logger(),
	}

	app.AddClient(uaid, &Client{
		Worker(worker),
		noPush,
		uaid})
	resp := httptest.NewRecorder()
	// don't bother with encryption right now.
	key, _ := app.Store().IDsToKey(uaid, chid)
	req, err := http.NewRequest("PUT",
		fmt.Sprintf("http://test/update/%s", key),
		nil)
	if req == nil {
		t.Fatal("Update put returned nil")
	}
	if err != nil {
		t.Fatal(err)
	}
	req.Form = make(url.Values)
	req.Form.Add("version", "1")
	req.Form.Add("data", data)
	tmux := mux.NewRouter()

	// Yay! Actually try the test!
	tmux.HandleFunc("/update/{key}", handler.UpdateHandler)
	tmux.ServeHTTP(resp, req)
	if resp.Body.String() != "{}" {
		t.Error("Unexpected response from server")
	}
	rep := TFlushReply{}
	if err = json.Unmarshal(worker.Outbuffer, &rep); err != nil {
		t.Errorf("Could not read output buffer %s", err.Error())
	}
	if rep.Data != data {
		t.Error("Returned data does not match expected value")
	}
	if rep.Version != 1 {
		t.Error("Returned version does not match expected value")
	}
}
