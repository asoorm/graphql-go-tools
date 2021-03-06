// Code generated by MockGen. DO NOT EDIT.
// Source: prisma.go

// Package stage2 is a generated GoMock package.
package stage2

import (
	gomock "github.com/golang/mock/gomock"
	reflect "reflect"
)

// MockPrisma is a mock of Prisma interface
type MockPrisma struct {
	ctrl     *gomock.Controller
	recorder *MockPrismaMockRecorder
}

// MockPrismaMockRecorder is the mock recorder for MockPrisma
type MockPrismaMockRecorder struct {
	mock *MockPrisma
}

// NewMockPrisma creates a new mock instance
func NewMockPrisma(ctrl *gomock.Controller) *MockPrisma {
	mock := &MockPrisma{ctrl: ctrl}
	mock.recorder = &MockPrismaMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use
func (m *MockPrisma) EXPECT() *MockPrismaMockRecorder {
	return m.recorder
}

// Query mocks base method
func (m *MockPrisma) Query(request []byte) []byte {
	ret := m.ctrl.Call(m, "Query", request)
	ret0, _ := ret[0].([]byte)
	return ret0
}

// Query indicates an expected call of Query
func (mr *MockPrismaMockRecorder) Query(request interface{}) *gomock.Call {
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Query", reflect.TypeOf((*MockPrisma)(nil).Query), request)
}
