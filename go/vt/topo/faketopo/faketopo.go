/*
Copyright 2021 The Vitess Authors.

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

package faketopo

import (
	"context"
	"strings"
	"sync"
	"time"

	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/topo"
	"vitess.io/vitess/go/vt/topo/memorytopo"

	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
)

// FakeFactory implements the Factory interface. This is supposed to be used only for testing
type FakeFactory struct {
	// mu protects the following field.
	mu sync.Mutex
	// cells is the toplevel map that has one entry per cell. It has a list of connections that this fake server will return
	cells map[string][]*FakeConn
}

var _ topo.Factory = (*FakeFactory)(nil)

// NewFakeTopoFactory creates a new fake topo factory
func NewFakeTopoFactory() *FakeFactory {
	factory := &FakeFactory{
		mu:    sync.Mutex{},
		cells: map[string][]*FakeConn{},
	}
	factory.cells[topo.GlobalCell] = []*FakeConn{NewFakeConnection()}
	return factory
}

// AddCell is used to add a cell to the factory. It returns the fake connection created. This connection can then be used to set get and update errors
func (f *FakeFactory) AddCell(cell string) *FakeConn {
	f.mu.Lock()
	defer f.mu.Unlock()
	conn := NewFakeConnection()
	f.cells[cell] = []*FakeConn{conn}
	return conn
}

// SetCell is used to set a cell in the factory.
func (f *FakeFactory) SetCell(cell string, fakeConn *FakeConn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cells[cell] = []*FakeConn{fakeConn}
}

// HasGlobalReadOnlyCell implements the Factory interface
func (f *FakeFactory) HasGlobalReadOnlyCell(serverAddr, root string) bool {
	return false
}

// Create implements the Factory interface
// It creates a fake connection which is supposed to be used only for testing
func (f *FakeFactory) Create(cell, serverAddr, root string) (topo.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	connections, ok := f.cells[cell]
	if !ok || len(connections) == 0 {
		return nil, topo.NewError(topo.NoNode, cell)
	}
	// pick the first connection and remove it from the list.
	conn := connections[0]
	f.cells[cell] = connections[1:]

	conn.serverAddr = serverAddr
	conn.cell = cell
	return conn, nil
}

// FakeConn implements the Conn interface. It is used only for testing
type FakeConn struct {
	cell       string
	serverAddr string

	// mutex to protect all the operations.
	mu sync.Mutex

	// getResultMap is a map storing the results for each filepath.
	getResultMap map[string]result
	// listResultMap is a map storing the resuls for each filepath prefix.
	listResultMap map[string][]topo.KVInfo
	// updateErrors stores whether update function call should error or not.
	updateErrors []updateError
	// getErrors stores whether the get function call should error or not.
	getErrors []bool
	// listErrors stores whether the list function call should error or not.
	listErrors []bool

	// watches is a map of all watches for this connection to the cell keyed by the filepath.
	watches map[string][]chan *topo.WatchData
}

// updateError contains the information whether a update call should return an error or not
// it also stores if the current write should persist or not
type updateError struct {
	shouldError   bool
	writePersists bool
}

// NewFakeConnection creates a new fake connection
func NewFakeConnection() *FakeConn {
	return &FakeConn{
		getResultMap:  map[string]result{},
		listResultMap: map[string][]topo.KVInfo{},
		watches:       map[string][]chan *topo.WatchData{},
		getErrors:     []bool{},
		listErrors:    []bool{},
		updateErrors:  []updateError{},
	}
}

// AddGetError is used to add a get error to the fake connection
func (f *FakeConn) AddGetError(shouldErr bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getErrors = append(f.getErrors, shouldErr)
}

// AddListError is used to add a list error to the fake connection
func (f *FakeConn) AddListError(shouldErr bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listErrors = append(f.listErrors, shouldErr)
}

// AddListResult is used to add a list result to the fake connection
func (f *FakeConn) AddListResult(filePathPrefix string, result []topo.KVInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listResultMap[filePathPrefix] = result
}

// AddUpdateError is used to add an update error to the fake connection
func (f *FakeConn) AddUpdateError(shouldErr bool, writePersists bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateErrors = append(f.updateErrors, updateError{
		shouldError:   shouldErr,
		writePersists: writePersists,
	})
}

// result keeps track of the fields needed to respond to a Get function call
type result struct {
	contents []byte
	version  uint64
}

var _ topo.Conn = (*FakeConn)(nil)

// ListDir implements the Conn interface
func (f *FakeConn) ListDir(ctx context.Context, dirPath string, full bool) ([]topo.DirEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var res []topo.DirEntry

	for filePath := range f.getResultMap {
		if strings.HasPrefix(filePath, dirPath) {
			remaining := filePath[len(dirPath)+1:]
			idx := strings.Index(remaining, "/")
			if idx == -1 {
				res = addToListOfDirEntries(res, topo.DirEntry{
					Name: remaining,
					Type: topo.TypeFile,
				})
			} else {
				res = addToListOfDirEntries(res, topo.DirEntry{
					Name: remaining[0:idx],
					Type: topo.TypeDirectory,
				})
			}
		}
	}

	if len(res) == 0 {
		return nil, topo.NewError(topo.NoNode, dirPath)
	}
	return res, nil
}

func addToListOfDirEntries(list []topo.DirEntry, elem topo.DirEntry) []topo.DirEntry {
	for _, entry := range list {
		if entry.Name == elem.Name {
			return list
		}
	}
	list = append(list, elem)
	return list
}

// Create implements the Conn interface
func (f *FakeConn) Create(ctx context.Context, filePath string, contents []byte) (topo.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getResultMap[filePath] = result{
		contents: contents,
		version:  1,
	}
	return memorytopo.NodeVersion(1), nil
}

// Update implements the Conn interface
func (f *FakeConn) Update(ctx context.Context, filePath string, contents []byte, version topo.Version) (topo.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	shouldErr := false
	writeSucceeds := true
	if len(f.updateErrors) > 0 {
		shouldErr = f.updateErrors[0].shouldError
		writeSucceeds = f.updateErrors[0].writePersists
		f.updateErrors = f.updateErrors[1:]
	}
	if version == nil {
		f.getResultMap[filePath] = result{
			contents: contents,
			version:  1,
		}
		return memorytopo.NodeVersion(1), nil
	}
	res, isPresent := f.getResultMap[filePath]
	if !isPresent {
		return nil, topo.NewError(topo.NoNode, filePath)
	}
	if writeSucceeds {
		res.contents = contents
		f.getResultMap[filePath] = res
	}
	if shouldErr {
		return nil, topo.NewError(topo.Timeout, filePath)
	}

	// Call the watches
	for path, watches := range f.watches {
		if path != filePath {
			continue
		}
		for _, watch := range watches {
			watch <- &topo.WatchData{
				Contents: res.contents,
				Version:  memorytopo.NodeVersion(res.version),
			}
		}
	}
	return memorytopo.NodeVersion(res.version), nil
}

// Get implements the Conn interface
func (f *FakeConn) Get(ctx context.Context, filePath string) ([]byte, topo.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.getErrors) > 0 {
		shouldErr := f.getErrors[0]
		f.getErrors = f.getErrors[1:]
		if shouldErr {
			return nil, nil, topo.NewError(topo.Timeout, filePath)
		}
	}
	res, isPresent := f.getResultMap[filePath]
	if !isPresent {
		return nil, nil, topo.NewError(topo.NoNode, filePath)
	}
	return res.contents, memorytopo.NodeVersion(res.version), nil
}

// GetVersion is part of topo.Conn interface.
func (f *FakeConn) GetVersion(ctx context.Context, filePath string, version int64) ([]byte, error) {
	return nil, topo.NewError(topo.NoImplementation, "GetVersion not supported in fake topo")
}

// List is part of the topo.Conn interface.
func (f *FakeConn) List(ctx context.Context, filePathPrefix string) ([]topo.KVInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.listErrors) > 0 {
		shouldErr := f.listErrors[0]
		f.listErrors = f.listErrors[1:]
		if shouldErr {
			return nil, topo.NewError(topo.Timeout, filePathPrefix)
		}
	}
	kvInfos, isPresent := f.listResultMap[filePathPrefix]
	if !isPresent {
		return nil, topo.NewError(topo.NoNode, filePathPrefix)
	}
	return kvInfos, nil
}

// Delete implements the Conn interface
func (f *FakeConn) Delete(ctx context.Context, filePath string, version topo.Version) error {
	panic("implement me")
}

// fakeLockDescriptor implements the topo.LockDescriptor interface
type fakeLockDescriptor struct {
}

// Check implements the topo.LockDescriptor interface
func (f fakeLockDescriptor) Check(ctx context.Context) error {
	return nil
}

// Unlock implements the topo.LockDescriptor interface
func (f fakeLockDescriptor) Unlock(ctx context.Context) error {
	return nil
}

var _ topo.LockDescriptor = (*fakeLockDescriptor)(nil)

// Lock implements the Conn interface
func (f *FakeConn) Lock(ctx context.Context, dirPath, contents string) (topo.LockDescriptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &fakeLockDescriptor{}, nil
}

// LockWithTTL implements the Conn interface.
func (f *FakeConn) LockWithTTL(ctx context.Context, dirPath, contents string, _ time.Duration) (topo.LockDescriptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &fakeLockDescriptor{}, nil
}

// LockName implements the Conn interface.
func (f *FakeConn) LockName(ctx context.Context, dirPath, contents string) (topo.LockDescriptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &fakeLockDescriptor{}, nil
}

// TryLock is part of the topo.Conn interface. Its implementation is same as Lock
func (f *FakeConn) TryLock(ctx context.Context, dirPath, contents string) (topo.LockDescriptor, error) {
	return f.Lock(ctx, dirPath, contents)
}

// Watch implements the Conn interface
func (f *FakeConn) Watch(ctx context.Context, filePath string) (*topo.WatchData, <-chan *topo.WatchData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, isPresent := f.getResultMap[filePath]
	if !isPresent {
		return nil, nil, topo.NewError(topo.NoNode, filePath)
	}
	current := &topo.WatchData{
		Contents: res.contents,
		Version:  memorytopo.NodeVersion(res.version),
	}

	notifications := make(chan *topo.WatchData, 100)
	f.watches[filePath] = append(f.watches[filePath], notifications)

	go func() {
		<-ctx.Done()
		watches, isPresent := f.watches[filePath]
		if !isPresent {
			return
		}
		for i, watch := range watches {
			if notifications == watch {
				close(notifications)
				f.watches[filePath] = append(watches[0:i], watches[i+1:]...)
				break
			}
		}
	}()
	return current, notifications, nil
}

func (f *FakeConn) WatchRecursive(ctx context.Context, path string) ([]*topo.WatchDataRecursive, <-chan *topo.WatchDataRecursive, error) {
	panic("implement me")
}

// NewLeaderParticipation implements the Conn interface
func (f *FakeConn) NewLeaderParticipation(string, string) (topo.LeaderParticipation, error) {
	panic("implement me")
}

// Close implements the Conn interface
func (f *FakeConn) Close() {
	panic("implement me")
}

// NewFakeTopoServer creates a new fake topo server
func NewFakeTopoServer(ctx context.Context, factory *FakeFactory) *topo.Server {
	ts, err := topo.NewWithFactory(factory, "" /*serverAddress*/, "" /*root*/)
	if err != nil {
		log.Exitf("topo.NewWithFactory() failed: %v", err)
	}
	for cell := range factory.cells {
		if err := ts.CreateCellInfo(ctx, cell, &topodatapb.CellInfo{}); err != nil {
			log.Exitf("ts.CreateCellInfo(%v) failed: %v", cell, err)
		}
	}
	return ts
}
