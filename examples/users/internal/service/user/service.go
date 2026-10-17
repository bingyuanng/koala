// Package user implements the foundation UserService fixture.
package user

import (
	"context"
	"fmt"

	userv1 "github.com/bingyuanng/koala/examples/users/gen/go/user/v1"
	"github.com/bingyuanng/koala/examples/users/internal/deps"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service struct {
	userv1.UnimplementedUserServiceServer
	users *deps.Users
}

var _ userv1.UserServiceServer = (*Service)(nil)

//koala:service user.v1.UserService
func New(d *deps.Dependencies) (*Service, error) {
	if d == nil || d.Users == nil {
		return nil, fmt.Errorf("user service requires a users repository")
	}
	return &Service{users: d.Users}, nil
}

// CreateUser assumes the runtime validates the request before dispatch.
// Central request validation is added in Phase 6.
func (s *Service) CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.CreateUserResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	user := s.users.Create(req.GetName(), req.GetEmail())
	return &userv1.CreateUserResponse{User: transportUser(user)}, nil
}

func (s *Service) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.GetUserResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	user, ok := s.users.Get(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, "user not found")
	}
	return &userv1.GetUserResponse{User: transportUser(user)}, nil
}

func transportUser(user deps.User) *userv1.User {
	return &userv1.User{Id: user.ID, Name: user.Name, Email: user.Email}
}
