/*
Copyright 2025 The Volcano Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package framework

import "sync"

type StorageInterface interface {
	Get(name string) (any, bool)

	Set(name string, object any)

	Delete(name string)
}

type Storage struct {
	lock sync.RWMutex
	data map[string]any
}

var storage Storage

func GetStorage() StorageInterface {
	return &storage
}

func initializeStorage() {
	storage.data = make(map[string]any)
}

func (s *Storage) Get(name string) (any, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	value, ok := s.data[name]
	return value, ok
}

func (s *Storage) Set(name string, object any) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.data == nil {
		s.data = make(map[string]any)
	}

	s.data[name] = object
}

func (s *Storage) Delete(name string) {
	s.lock.Lock()
	defer s.lock.Unlock()

	delete(s.data, name)
}
