package jellyfinbridge

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"

	"novastream/internal/jellyfinbridge/dto"
)

// Version items. Jellyfin gives every version of a movie or episode an item
// of its own, whose ID is the version's media source ID. Clients rely on
// that: Android TV loads the version picked on the details page as an item,
// queues that item and plays the media source whose ID is the item ID;
// jellyfin-web loads it as well. The bridge answers a version ID with its
// item under the version's ID, and every other request for that ID (playback,
// reports, images, user data) as one for the item.

type versionCtxKey struct{}

// versionAliases routes requests for a version item to its item: the {id}
// path variable becomes the item ID, and handlers that answer with the item
// itself find the version that was asked for with requestedVersion.
func (s *Server) versionAliases(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		if id := vars["id"]; id != "" {
			if itemID, ok := s.versionItem(id); ok {
				aliased := make(map[string]string, len(vars))
				for k, v := range vars {
					aliased[k] = v
				}
				aliased["id"] = itemID
				r = r.WithContext(context.WithValue(r.Context(), versionCtxKey{}, normalizeID(id)))
				r = mux.SetURLVars(r, aliased)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requestedVersion is the version ID a request addressed, if any.
func requestedVersion(r *http.Request) string {
	v, _ := r.Context().Value(versionCtxKey{}).(string)
	return v
}

// itemIDOf maps a version ID from a request body or query to its item.
func (s *Server) itemIDOf(id string) string {
	if itemID, ok := s.versionItem(id); ok {
		return itemID
	}
	return normalizeID(id)
}

// asVersion turns an item into its version item versionID.
func asVersion(item *dto.BaseItemDto, versionID string) {
	item.ID = versionID
	if item.UserData != nil {
		ud := *item.UserData
		ud.ItemID = versionID
		item.UserData = &ud
	}
	for _, ms := range item.MediaSources {
		if ms.ID == versionID {
			return
		}
	}
	item.MediaSources = append(item.MediaSources, versionStub(versionID, "mediastorm", item.RunTimeTicks))
}
