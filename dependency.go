package huma

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/gin-gonic/gin"
)

// ErrDependencyInvalid is returned when registering a dependency fails.
var ErrDependencyInvalid = errors.New("dependency invalid")

// Dependency represents a handler function dependency and its associated
// inputs and outputs. Value can be either a struct pointer (global dependency)
// or a `func(dependencies, params) (headers, struct pointer, error)` style
// function.
type Dependency struct {
	Depends         []*Dependency
	Params          []*Param
	ResponseHeaders []*Header
	Value           interface{}
}

var contextDependency Dependency
var operationDependency Dependency

// ContextDependency returns a dependency for the current request's
// `*gin.Context`.
func ContextDependency() *Dependency {
	return &contextDependency
}

// OperationDependency returns a dependency  for the current `*huma.Operation`.
func OperationDependency() *Dependency {
	return &operationDependency
}

// validate that the dependency deps/params/headers match the function
// signature or that the value is not a function.
func (d *Dependency) validate(returnType reflect.Type) error {
	if d == &contextDependency || d == &operationDependency {
		// Hard-coded known dependencies. These are special and have no value.
		return nil
	}

	if d.Value == nil {
		return fmt.Errorf("value must be set: %w", ErrDependencyInvalid)
	}

	v := reflect.ValueOf(d.Value)

	if v.Kind() != reflect.Func {
		if returnType != nil && returnType != v.Type() {
			return fmt.Errorf("return type should be %s but got %s: %w", v.Type(), returnType, ErrDependencyInvalid)
		}

		// This is just a static value. It shouldn't have params/headers/etc.
		if len(d.Params) > 0 {
			return fmt.Errorf("global dependency should not have params: %w", ErrDependencyInvalid)
		}

		if len(d.ResponseHeaders) > 0 {
			return fmt.Errorf("global dependency should not set headers: %w", ErrDependencyInvalid)
		}

		return nil
	}

	fn := v.Type()
	lenArgs := len(d.Depends) + len(d.Params)
	if fn.NumIn() != lenArgs {
		// TODO: generate suggested func signature
		return fmt.Errorf("function signature should have %d args but got %s: %w", lenArgs, fn, ErrDependencyInvalid)
	}

	for _, dep := range d.Depends {
		if err := dep.validate(nil); err != nil {
			return err
		}
	}

	for i, p := range d.Params {
		if err := validateParam(p, fn.In(len(d.Depends)+i)); err != nil {
			return err
		}
	}

	lenReturn := len(d.ResponseHeaders) + 2
	if fn.NumOut() != lenReturn {
		return fmt.Errorf("function should return %d values but got %d: %w", lenReturn, fn.NumOut(), ErrDependencyInvalid)
	}

	for i, h := range d.ResponseHeaders {
		if err := validateHeader(h, fn.Out(i)); err != nil {
			return err
		}
	}

	return nil
}

// AllParams returns all parameters for all dependencies in the graph of this
// dependency in depth-first order without duplicates.
func (d *Dependency) AllParams() []*Param {
	params := []*Param{}
	seen := map[*Param]bool{}

	for _, p := range d.Params {
		seen[p] = true
		params = append(params, p)
	}

	for _, d := range d.Depends {
		for _, p := range d.AllParams() {
			if _, ok := seen[p]; !ok {
				seen[p] = true

				params = append(params, p)
			}
		}
	}

	return params
}

// AllResponseHeaders returns all response headers for all dependencies in
// the graph of this dependency in depth-first order without duplicates.
func (d *Dependency) AllResponseHeaders() []*Header {
	headers := []*Header{}
	seen := map[*Header]bool{}

	for _, h := range d.ResponseHeaders {
		seen[h] = true
		headers = append(headers, h)
	}

	for _, d := range d.Depends {
		for _, h := range d.AllResponseHeaders() {
			if _, ok := seen[h]; !ok {
				seen[h] = true

				headers = append(headers, h)
			}
		}
	}

	return headers
}

// Resolve the value of the dependency. Returns (response headers, value, error).
func (d *Dependency) Resolve(c *gin.Context, op *Operation) (map[string]string, interface{}, error) {
	// Identity dependencies are first. Just return if it's one of them.
	if d == &contextDependency {
		return nil, c, nil
	}

	if d == &operationDependency {
		return nil, op, nil
	}

	v := reflect.ValueOf(d.Value)
	if v.Kind() != reflect.Func {
		// Not a function, just return the global value.
		return nil, d.Value, nil
	}

	// Generate the input arguments
	in := make([]reflect.Value, 0, v.Type().NumIn())
	headers := map[string]string{}

	// Resolve each sub-dependency
	for _, dep := range d.Depends {
		dHeaders, dVal, err := dep.Resolve(c, op)
		if err != nil {
			return nil, nil, err
		}

		for h, hv := range dHeaders {
			headers[h] = hv
		}

		in = append(in, reflect.ValueOf(dVal))
	}

	// Get each input parameter
	for _, param := range d.Params {
		v, err := getParamValue(c, param)
		if err != nil {
			return nil, nil, err
		}

		in = append(in, reflect.ValueOf(v))
	}

	// Call the function.
	out := v.Call(in)

	if last := out[len(out)-1]; !last.IsNil() {
		// There was an error!
		return nil, nil, last.Interface().(error)
	}

	// Get the headers & response value.
	for i, h := range d.ResponseHeaders {
		headers[h.Name] = out[i].Interface().(string)
	}

	return headers, out[len(d.ResponseHeaders)].Interface(), nil
}
