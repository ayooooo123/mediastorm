package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"novastream/handlers"
	"novastream/models"
	"novastream/services/customlists"

	"github.com/gorilla/mux"
)

// fakeCustomListsService implements customListsService for testing.
type fakeCustomListsService struct {
	lists      []models.CustomList
	listErr    error
	createList models.CustomList
	createErr  error
	renameList models.CustomList
	renameErr  error
	deleted    bool
	deleteErr  error
	items      []models.WatchlistItem
	itemsErr   error
	addItem    models.WatchlistItem
	addErr     error
	removed    bool
	removeErr  error
}

func (f *fakeCustomListsService) ListLists(userID string) ([]models.CustomList, error) {
	return f.lists, f.listErr
}

func (f *fakeCustomListsService) CreateList(userID, name string) (models.CustomList, error) {
	return f.createList, f.createErr
}

func (f *fakeCustomListsService) RenameList(userID, listID, name string) (models.CustomList, error) {
	return f.renameList, f.renameErr
}

func (f *fakeCustomListsService) DeleteList(userID, listID string) (bool, error) {
	return f.deleted, f.deleteErr
}

func (f *fakeCustomListsService) ListItems(userID, listID string) ([]models.WatchlistItem, error) {
	return f.items, f.itemsErr
}

func (f *fakeCustomListsService) AddItem(userID, listID string, input models.WatchlistUpsert) (models.WatchlistItem, error) {
	return f.addItem, f.addErr
}

func (f *fakeCustomListsService) RemoveItem(userID, listID, mediaType, id string) (bool, error) {
	return f.removed, f.removeErr
}

func customListsRequest(method, path string, body any, vars map[string]string) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	if len(vars) > 0 {
		r = mux.SetURLVars(r, vars)
	}
	return r
}

func TestCustomListsHandler_ListLists_Success(t *testing.T) {
	expected := []models.CustomList{{ID: "list-1", Name: "My List"}}
	svc := &fakeCustomListsService{lists: expected}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	r := customListsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.ListLists(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestCustomListsHandler_ListLists_MissingUserID(t *testing.T) {
	svc := &fakeCustomListsService{}
	h := handlers.NewCustomListsHandler(svc, nil)

	r := customListsRequest(http.MethodGet, "/", nil, map[string]string{"userID": ""})
	w := httptest.NewRecorder()
	h.ListLists(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCustomListsHandler_ListLists_UserNotFound(t *testing.T) {
	svc := &fakeCustomListsService{}
	usersSvc := &fakeUserExistsService{exists: false}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	r := customListsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.ListLists(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCustomListsHandler_CreateList_Success(t *testing.T) {
	expected := models.CustomList{ID: "list-2", Name: "New List"}
	svc := &fakeCustomListsService{createList: expected}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	body := map[string]string{"name": "New List"}
	r := customListsRequest(http.MethodPost, "/", body, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.CreateList(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusCreated)
	}
}

func TestCustomListsHandler_CreateList_EmptyName(t *testing.T) {
	svc := &fakeCustomListsService{createErr: customlists.ErrListNameRequired}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	body := map[string]string{"name": ""}
	r := customListsRequest(http.MethodPost, "/", body, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.CreateList(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCustomListsHandler_CreateList_InvalidJSON(t *testing.T) {
	svc := &fakeCustomListsService{}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("{bad"))
	r = mux.SetURLVars(r, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.CreateList(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCustomListsHandler_RenameList_Success(t *testing.T) {
	expected := models.CustomList{ID: "list-1", Name: "Renamed"}
	svc := &fakeCustomListsService{renameList: expected}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	body := map[string]string{"name": "Renamed"}
	r := customListsRequest(http.MethodPut, "/", body,
		map[string]string{"userID": "u1", "listID": "list-1"})
	w := httptest.NewRecorder()
	h.RenameList(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestCustomListsHandler_RenameList_NotFound(t *testing.T) {
	svc := &fakeCustomListsService{renameErr: os.ErrNotExist}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	body := map[string]string{"name": "Renamed"}
	r := customListsRequest(http.MethodPut, "/", body,
		map[string]string{"userID": "u1", "listID": "list-1"})
	w := httptest.NewRecorder()
	h.RenameList(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCustomListsHandler_DeleteList_Success(t *testing.T) {
	svc := &fakeCustomListsService{deleted: true}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	r := customListsRequest(http.MethodDelete, "/", nil,
		map[string]string{"userID": "u1", "listID": "list-1"})
	w := httptest.NewRecorder()
	h.DeleteList(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestCustomListsHandler_DeleteList_NotFound(t *testing.T) {
	svc := &fakeCustomListsService{deleted: false}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	r := customListsRequest(http.MethodDelete, "/", nil,
		map[string]string{"userID": "u1", "listID": "list-1"})
	w := httptest.NewRecorder()
	h.DeleteList(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCustomListsHandler_ListItems_Success(t *testing.T) {
	expected := []models.WatchlistItem{{ID: "item-1"}}
	svc := &fakeCustomListsService{items: expected}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	r := customListsRequest(http.MethodGet, "/", nil,
		map[string]string{"userID": "u1", "listID": "list-1"})
	w := httptest.NewRecorder()
	h.ListItems(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestCustomListsHandler_ListItems_NotFound(t *testing.T) {
	svc := &fakeCustomListsService{itemsErr: os.ErrNotExist}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	r := customListsRequest(http.MethodGet, "/", nil,
		map[string]string{"userID": "u1", "listID": "list-1"})
	w := httptest.NewRecorder()
	h.ListItems(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCustomListsHandler_AddItem_Success(t *testing.T) {
	expected := models.WatchlistItem{ID: "item-1"}
	svc := &fakeCustomListsService{addItem: expected}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	body := models.WatchlistUpsert{ID: "tmdb-123", MediaType: "movie", Name: "Test"}
	r := customListsRequest(http.MethodPost, "/", body,
		map[string]string{"userID": "u1", "listID": "list-1"})
	w := httptest.NewRecorder()
	h.AddItem(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestCustomListsHandler_AddItem_MissingFields(t *testing.T) {
	svc := &fakeCustomListsService{addErr: customlists.ErrIDRequired}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	body := models.WatchlistUpsert{}
	r := customListsRequest(http.MethodPost, "/", body,
		map[string]string{"userID": "u1", "listID": "list-1"})
	w := httptest.NewRecorder()
	h.AddItem(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCustomListsHandler_AddItem_ListNotFound(t *testing.T) {
	svc := &fakeCustomListsService{addErr: os.ErrNotExist}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	body := models.WatchlistUpsert{ID: "tmdb-123", MediaType: "movie", Name: "Test"}
	r := customListsRequest(http.MethodPost, "/", body,
		map[string]string{"userID": "u1", "listID": "list-1"})
	w := httptest.NewRecorder()
	h.AddItem(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCustomListsHandler_RemoveItem_Success(t *testing.T) {
	svc := &fakeCustomListsService{removed: true}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	r := customListsRequest(http.MethodDelete, "/", nil,
		map[string]string{"userID": "u1", "listID": "list-1", "mediaType": "movie", "id": "tmdb-123"})
	w := httptest.NewRecorder()
	h.RemoveItem(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestCustomListsHandler_RemoveItem_NotFound(t *testing.T) {
	svc := &fakeCustomListsService{removed: false}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewCustomListsHandler(svc, usersSvc)

	r := customListsRequest(http.MethodDelete, "/", nil,
		map[string]string{"userID": "u1", "listID": "list-1", "mediaType": "movie", "id": "tmdb-123"})
	w := httptest.NewRecorder()
	h.RemoveItem(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCustomListsHandler_Options(t *testing.T) {
	h := handlers.NewCustomListsHandler(&fakeCustomListsService{}, nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/", nil)
	h.Options(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}
