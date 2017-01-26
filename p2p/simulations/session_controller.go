package simulations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"reflect"
	"sync"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/logger/glog"
	"github.com/ethereum/go-ethereum/p2p/adapters"
)

type returnHandler func(body io.Reader) (resp io.ReadSeeker, err error)

type ResourceHandler struct {
	Handle func(interface{}, *ResourceController) (interface{}, error)
	Type   reflect.Type
}

type ResourceHandlers struct {
	Create, Retrieve, Update, Destroy *ResourceHandler
}

type ResourceController struct {
	lock        sync.Mutex
	controllers map[string]Controller
	id          int
	methods     []string
	*ResourceHandlers
}

var methodsAvailable = []string{"POST", "GET", "PUT", "DELETE"}

func (self *ResourceHandlers) handler(method string) *ResourceHandler {
	var h *ResourceHandler
	switch method {
	case "POST":
		h = self.Create
	case "GET":
		h = self.Retrieve
	case "PUT":
		h = self.Update
	case "DELETE":
		h = self.Destroy
	}
	return h
}

func NewResourceContoller(c *ResourceHandlers) *ResourceController {
	var methods []string
	for _, method := range methodsAvailable {
		if c.handler(method) != nil {
			methods = append(methods, method)
		}
	}
	return &ResourceController{
		ResourceHandlers: c,
		controllers:      make(map[string]Controller),
		methods:          methods,
	}
}

var empty = struct{}{}

func NewSessionController() (*ResourceController, chan bool) {
	quitc := make(chan bool)
	return NewResourceContoller(
		&ResourceHandlers{

			Create: &ResourceHandler{
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					conf := msg.(*NetworkConfig)
					m := NewNetworkController(conf, &event.TypeMux{}, NewJournal())
					if len(conf.Id) == 0 {
						conf.Id = fmt.Sprintf("%d", parent.id)
					}
					glog.V(6).Infof("new network controller on %v", conf.Id)
					if parent != nil {
						parent.SetResource(conf.Id, m)
					}
					parent.id++
					return empty, nil
				},
				Type: reflect.TypeOf(&NetworkConfig{}),
			},

			Destroy: &ResourceHandler{
				Handle: func(msg interface{}, parent *ResourceController) (interface{}, error) {
					glog.V(6).Infof("destroy handler called")
					// this can quit the entire app (shut down the backend server)
					quitc <- true
					return empty, nil
				},
			},
		},
	), quitc
}

func (self *ResourceController) Handle(method string) (returnHandler, error) {
	h := self.handler(method)
	if h == nil {
		return nil, fmt.Errorf("allowed methods: %v", self.methods)
	}
	// glog.V(6).Infof("get handler callback for method %v", method)
	rh := func(r io.Reader) (io.ReadSeeker, error) {
		input, err := ioutil.ReadAll(r)
		if err != nil {
			// glog.V(6).Infof("reading json body: %v", err)
			return nil, err
		}
		// glog.V(6).Infof("decode json request body")
		var arg interface{}
		if len(input) == 0 {
			input = []byte("{}")
		}
		if h.Type != nil {
			val := reflect.New(h.Type)
			req := val.Elem()
			req.Set(reflect.Zero(h.Type))
			err = json.Unmarshal(input, val.Interface())
			if err != nil {
				return nil, err
			}
			arg = req.Interface()
		}
		res, err := h.Handle(arg, self)
		if err != nil {
			return nil, err
		}
		resp, err := json.MarshalIndent(res, "", "  ")
		return bytes.NewReader(resp), nil
	}
	return rh, nil
}

func (self *ResourceController) Resource(id string) (Controller, error) {
	self.lock.Lock()
	defer self.lock.Unlock()
	c, ok := self.controllers[id]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return c, nil
}

func (self *ResourceController) SetResource(id string, c Controller) {
	self.lock.Lock()
	defer self.lock.Unlock()
	if c == nil {
		delete(self.controllers, id)
	} else {
		self.controllers[id] = c
	}
}

func (self *ResourceController) DeleteResource(id string) {
	delete(self.controllers, id)
}

func RandomNodeId() *adapters.NodeId {
	key, err := crypto.GenerateKey()
	if err != nil {
		panic("unable to generate key")
	}
	pubkey := crypto.FromECDSAPub(&key.PublicKey)
	return adapters.NewNodeId(pubkey[1:])
}

func RandomNodeIds(n int) []*adapters.NodeId {
	var ids []*adapters.NodeId
	for i := 0; i < n; i++ {
		ids = append(ids, RandomNodeId())
	}
	return ids
}
