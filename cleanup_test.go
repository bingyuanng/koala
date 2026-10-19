package koala_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	koala "github.com/bingyuanng/koala"
)

func TestCloseReversesResourcesAndPreservesErrors(t *testing.T) {
	failure := errors.New("resource failed")
	var order []int
	var deadline time.Time
	record := func(ctx context.Context, value int) {
		t.Helper()
		if ctx.Err() != nil {
			t.Fatal("cleanup started with canceled context")
		}
		got, ok := ctx.Deadline()
		if !ok {
			t.Fatal("cleanup has no deadline")
		}
		if deadline.IsZero() {
			deadline = got
		} else if !deadline.Equal(got) {
			t.Error("resources did not share a deadline")
		}
		order = append(order, value)
	}
	err := koala.Close("1s",
		func(ctx context.Context) error { record(ctx, 1); return nil },
		func(ctx context.Context) error { record(ctx, 2); return failure },
		func(ctx context.Context) error { record(ctx, 3); panic("private") },
	)
	if !reflect.DeepEqual(order, []int{3, 2, 1}) {
		t.Fatal(order)
	}
	if !errors.Is(err, failure) {
		t.Fatal("resource error lost", err)
	}
	if err == nil || err.Error() == failure.Error() {
		t.Fatal("panic error lost", err)
	}
}

func TestCloseContinuesAfterTimeout(t *testing.T) {
	called := false
	err := koala.Close("10ms",
		func(ctx context.Context) error {
			called = true
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Error(ctx.Err())
			}
			return nil
		},
		func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
	)
	if !called || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(called, err)
	}
}

func TestCloseRejectsInvalidBudget(t *testing.T) {
	for _, value := range []string{"invalid", "0s", "-1s"} {
		if err := koala.Close(value); err == nil {
			t.Fatal("invalid budget accepted", value)
		}
	}
}
