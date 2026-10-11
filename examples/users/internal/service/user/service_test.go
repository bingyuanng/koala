package user

import (
	"context"
	"fmt"
	"sync"
	"testing"

	userv1 "github.com/bingyuanng/koala/examples/users/gen/go/user/v1"
	"github.com/bingyuanng/koala/examples/users/internal/deps"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConcurrentCreateAndGet(t *testing.T) {
	service, err := New(deps.New())
	if err != nil {
		t.Fatal(err)
	}
	const count = 64
	ids := make(chan string, count)
	var calls sync.WaitGroup
	for i := range count {
		calls.Go(func() {
			name := fmt.Sprintf("user-%d", i)
			created, err := service.CreateUser(context.Background(), &userv1.CreateUserRequest{Name: name, Email: name + "@example.com"})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- created.User.Id
			// Mutating a response must not mutate the stored application value.
			created.User.Name = "changed"
			fetched, err := service.GetUser(context.Background(), &userv1.GetUserRequest{Id: created.User.Id})
			if err != nil {
				t.Error(err)
				return
			}
			if fetched.User.Name != name {
				t.Errorf("stored name = %q, want %q", fetched.User.Name, name)
			}
		})
	}
	calls.Wait()
	close(ids)
	seen := make(map[string]bool)
	for id := range ids {
		if id == "" || seen[id] {
			t.Errorf("empty or duplicate ID %q", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("created %d users, want %d", len(seen), count)
	}
}

func TestErrorsAndCancellation(t *testing.T) {
	for _, d := range []*deps.Dependencies{nil, {}} {
		if _, err := New(d); err == nil {
			t.Fatal("missing repository accepted")
		}
	}
	service, err := New(deps.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetUser(context.Background(), &userv1.GetUserRequest{Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing user: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.CreateUser(ctx, &userv1.CreateUserRequest{Name: "test"}); status.Code(err) != codes.Canceled {
		t.Fatalf("create cancellation: %v", err)
	}
	if _, err := service.GetUser(ctx, &userv1.GetUserRequest{Id: "1"}); status.Code(err) != codes.Canceled {
		t.Fatalf("get cancellation: %v", err)
	}
	created, err := service.CreateUser(context.Background(), &userv1.CreateUserRequest{Name: "first"})
	if err != nil || created.User.Id != "1" {
		t.Fatalf("canceled create stored a record: %v, %v", created, err)
	}
}
