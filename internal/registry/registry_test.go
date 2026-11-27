package registry_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/irobinson010/footman/internal/registry"
)

type greeter interface{ Greet() string }

type hello struct {
	Name string `json:"name"`
}

func (h hello) Greet() string { return "hello " + h.Name }

func newHello(cfg registry.Config) (greeter, error) {
	h := hello{Name: "world"}
	if err := cfg.Decode(&h); err != nil {
		return nil, err
	}
	return h, nil
}

func TestRegistry(t *testing.T) {
	r := registry.New[greeter]("greeter")
	r.Register("hello", newHello)

	if got := r.Names(); !reflect.DeepEqual(got, []string{"hello"}) {
		t.Fatalf("Names = %v", got)
	}
	if !r.Has("hello") || r.Has("nope") {
		t.Fatal("Has is wrong")
	}

	g, err := r.New("hello", nil)
	if err != nil || g.Greet() != "hello world" {
		t.Fatalf("New(nil) = %v, %v", g, err)
	}
	g, err = r.New("hello", registry.Map{"name": "Alex"})
	if err != nil || g.Greet() != "hello Alex" {
		t.Fatalf("New(Map) = %v, %v", g, err)
	}
	g, err = r.New("hello", registry.JSON(`{"name":"json"}`))
	if err != nil || g.Greet() != "hello json" {
		t.Fatalf("New(JSON) = %v, %v", g, err)
	}

	if _, err := r.New("hello", registry.Map{"nickname": "typo"}); err == nil {
		t.Fatal("unknown option key accepted")
	}
	if _, err := r.New("nope", nil); !errors.Is(err, registry.ErrUnknown) {
		t.Fatalf("New(unknown) err = %v, want ErrUnknown", err)
	}
}

func TestRegisterPanics(t *testing.T) {
	r := registry.New[greeter]("greeter")
	r.Register("hello", newHello)
	for name, f := range map[string]func(){
		"duplicate": func() { r.Register("hello", newHello) },
		"empty":     func() { r.Register("", newHello) },
		"nil":       func() { r.Register("x", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			f()
		})
	}
}
