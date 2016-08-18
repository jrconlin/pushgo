// Automatically generated by MockGen. DO NOT EDIT!
// Source: src/github.com/mozilla-services/pushgo/simplepush/handlers.go

package simplepush

import (
	http "net/http"
	net "net"
	gomock "github.com/rafrombrc/gomock/gomock"
)

// Mock of ServeMux interface
type MockServeMux struct {
	ctrl     *gomock.Controller
	recorder *_MockServeMuxRecorder
}

// Recorder for MockServeMux (not exported)
type _MockServeMuxRecorder struct {
	mock *MockServeMux
}

func NewMockServeMux(ctrl *gomock.Controller) *MockServeMux {
	mock := &MockServeMux{ctrl: ctrl}
	mock.recorder = &_MockServeMuxRecorder{mock}
	return mock
}

func (_m *MockServeMux) EXPECT() *_MockServeMuxRecorder {
	return _m.recorder
}

func (_m *MockServeMux) Handle(_param0 string, _param1 http.Handler) {
	_m.ctrl.Call(_m, "Handle", _param0, _param1)
}

func (_mr *_MockServeMuxRecorder) Handle(arg0, arg1 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "Handle", arg0, arg1)
}

func (_m *MockServeMux) HandleFunc(_param0 string, _param1 func(http.ResponseWriter, *http.Request)) {
	_m.ctrl.Call(_m, "HandleFunc", _param0, _param1)
}

func (_mr *_MockServeMuxRecorder) HandleFunc(arg0, arg1 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "HandleFunc", arg0, arg1)
}

func (_m *MockServeMux) ServeHTTP(_param0 http.ResponseWriter, _param1 *http.Request) {
	_m.ctrl.Call(_m, "ServeHTTP", _param0, _param1)
}

func (_mr *_MockServeMuxRecorder) ServeHTTP(arg0, arg1 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "ServeHTTP", arg0, arg1)
}

// Mock of Server interface
type MockServer struct {
	ctrl     *gomock.Controller
	recorder *_MockServerRecorder
}

// Recorder for MockServer (not exported)
type _MockServerRecorder struct {
	mock *MockServer
}

func NewMockServer(ctrl *gomock.Controller) *MockServer {
	mock := &MockServer{ctrl: ctrl}
	mock.recorder = &_MockServerRecorder{mock}
	return mock
}

func (_m *MockServer) EXPECT() *_MockServerRecorder {
	return _m.recorder
}

func (_m *MockServer) Serve(_param0 net.Listener) error {
	ret := _m.ctrl.Call(_m, "Serve", _param0)
	ret0, _ := ret[0].(error)
	return ret0
}

func (_mr *_MockServerRecorder) Serve(arg0 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "Serve", arg0)
}

func (_m *MockServer) Close() error {
	ret := _m.ctrl.Call(_m, "Close")
	ret0, _ := ret[0].(error)
	return ret0
}

func (_mr *_MockServerRecorder) Close() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "Close")
}

// Mock of Handler interface
type MockHandler struct {
	ctrl     *gomock.Controller
	recorder *_MockHandlerRecorder
}

// Recorder for MockHandler (not exported)
type _MockHandlerRecorder struct {
	mock *MockHandler
}

func NewMockHandler(ctrl *gomock.Controller) *MockHandler {
	mock := &MockHandler{ctrl: ctrl}
	mock.recorder = &_MockHandlerRecorder{mock}
	return mock
}

func (_m *MockHandler) EXPECT() *_MockHandlerRecorder {
	return _m.recorder
}

func (_m *MockHandler) Listener() net.Listener {
	ret := _m.ctrl.Call(_m, "Listener")
	ret0, _ := ret[0].(net.Listener)
	return ret0
}

func (_mr *_MockHandlerRecorder) Listener() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "Listener")
}

func (_m *MockHandler) MaxConns() int {
	ret := _m.ctrl.Call(_m, "MaxConns")
	ret0, _ := ret[0].(int)
	return ret0
}

func (_mr *_MockHandlerRecorder) MaxConns() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "MaxConns")
}

func (_m *MockHandler) URL() string {
	ret := _m.ctrl.Call(_m, "URL")
	ret0, _ := ret[0].(string)
	return ret0
}

func (_mr *_MockHandlerRecorder) URL() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "URL")
}

func (_m *MockHandler) ServeMux() ServeMux {
	ret := _m.ctrl.Call(_m, "ServeMux")
	ret0, _ := ret[0].(ServeMux)
	return ret0
}

func (_mr *_MockHandlerRecorder) ServeMux() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "ServeMux")
}

func (_m *MockHandler) Start(_param0 chan<- error) {
	_m.ctrl.Call(_m, "Start", _param0)
}

func (_mr *_MockHandlerRecorder) Start(arg0 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "Start", arg0)
}

func (_m *MockHandler) Close() error {
	ret := _m.ctrl.Call(_m, "Close")
	ret0, _ := ret[0].(error)
	return ret0
}

func (_mr *_MockHandlerRecorder) Close() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "Close")
}

// Mock of ListenerConfig interface
type MockListenerConfig struct {
	ctrl     *gomock.Controller
	recorder *_MockListenerConfigRecorder
}

// Recorder for MockListenerConfig (not exported)
type _MockListenerConfigRecorder struct {
	mock *MockListenerConfig
}

func NewMockListenerConfig(ctrl *gomock.Controller) *MockListenerConfig {
	mock := &MockListenerConfig{ctrl: ctrl}
	mock.recorder = &_MockListenerConfigRecorder{mock}
	return mock
}

func (_m *MockListenerConfig) EXPECT() *_MockListenerConfigRecorder {
	return _m.recorder
}

func (_m *MockListenerConfig) UseTLS() bool {
	ret := _m.ctrl.Call(_m, "UseTLS")
	ret0, _ := ret[0].(bool)
	return ret0
}

func (_mr *_MockListenerConfigRecorder) UseTLS() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "UseTLS")
}

func (_m *MockListenerConfig) GetMaxConns() int {
	ret := _m.ctrl.Call(_m, "GetMaxConns")
	ret0, _ := ret[0].(int)
	return ret0
}

func (_mr *_MockListenerConfigRecorder) GetMaxConns() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "GetMaxConns")
}

func (_m *MockListenerConfig) Listen() (net.Listener, error) {
	ret := _m.ctrl.Call(_m, "Listen")
	ret0, _ := ret[0].(net.Listener)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

func (_mr *_MockListenerConfigRecorder) Listen() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "Listen")
}
