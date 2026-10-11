// Package deps owns the example's shared application dependencies.
package deps

import (
	"fmt"
	"sync"
)

// User is an application value, independent of generated transport types.
type User struct {
	ID    string
	Name  string
	Email string
}

// Users is an in-memory repository shared by all service calls.
// It stores and returns values so callers cannot mutate stored records.
type Users struct {
	mu      sync.RWMutex
	nextID  uint64
	records map[string]User
}

func (r *Users) Create(name, email string) User {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.records == nil {
		r.records = make(map[string]User)
	}
	r.nextID++
	user := User{ID: fmt.Sprint(r.nextID), Name: name, Email: email}
	r.records[user.ID] = user
	return user
}

func (r *Users) Get(id string) (User, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	user, ok := r.records[id]
	return user, ok
}

type Dependencies struct {
	Users *Users
}

func New() *Dependencies {
	return &Dependencies{Users: &Users{}}
}
