// Created from Koala v0.1.0.
// This file belongs to the application. Generation must never overwrite it.
package service

import (
	"context"
	servicev1 "github.com/bingyuanng/koala/examples/users/gen/go/order/v1"
	"github.com/bingyuanng/koala/examples/users/internal/deps"
)

// OrderService inherits Unimplemented until application methods are added.
type OrderService struct {
	servicev1.UnimplementedOrderServiceServer
}

var _ servicev1.OrderServiceServer = (*OrderService)(nil)

//koala:service order.v1.OrderService
func New(d *deps.Dependencies) (*OrderService, error) {
	return &OrderService{}, nil
}

// Get demonstrates an application method reached by both generated transports.
func (s *OrderService) Get(ctx context.Context, r *servicev1.GetRequest) (*servicev1.GetResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &servicev1.GetResponse{Id: r.Id}, nil
}
