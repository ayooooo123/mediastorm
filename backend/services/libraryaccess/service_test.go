package libraryaccess

import (
	"context"
	"testing"

	"novastream/models"
)

type fakeAccessRepo struct {
	policies map[string]models.LibraryAccessPolicy
}
type fakeLocalItems struct{ item *models.LocalMediaItem }

func (f fakeLocalItems) GetItem(_ context.Context, id string) (*models.LocalMediaItem, error) {
	if f.item != nil && f.item.ID == id {
		return f.item, nil
	}
	return nil, nil
}

func (f *fakeAccessRepo) Get(_ context.Context, libraryID string) (*models.LibraryAccessPolicy, error) {
	policy, ok := f.policies[libraryID]
	if !ok {
		return nil, nil
	}
	copy := policy
	return &copy, nil
}
func (f *fakeAccessRepo) List(_ context.Context) (map[string]models.LibraryAccessPolicy, error) {
	return f.policies, nil
}
func (f *fakeAccessRepo) Set(_ context.Context, policy models.LibraryAccessPolicy) error {
	if f.policies == nil {
		f.policies = make(map[string]models.LibraryAccessPolicy)
	}
	f.policies[policy.LibraryID] = policy
	return nil
}
func (f *fakeAccessRepo) Delete(_ context.Context, libraryID string) error {
	delete(f.policies, libraryID)
	return nil
}

func TestCanAccessDefaultsMissingPolicyToRestricted(t *testing.T) {
	service := New(&fakeAccessRepo{policies: map[string]models.LibraryAccessPolicy{}}, nil, nil)
	allowed, err := service.CanAccess(context.Background(), "library-1", "account-1", "profile-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("missing policy must fail closed")
	}
}

func TestCanAccessSupportsAllAccountProfileAndMaster(t *testing.T) {
	repo := &fakeAccessRepo{policies: map[string]models.LibraryAccessPolicy{
		"all": {LibraryID: "all", AccessMode: models.LibraryAccessModeAll},
		"restricted": {
			LibraryID:         "restricted",
			AccessMode:        models.LibraryAccessModeRestricted,
			AllowedAccountIDs: []string{"account-1"},
			AllowedProfileIDs: []string{"profile-2"},
		},
		"empty-restricted": {
			LibraryID:  "empty-restricted",
			AccessMode: models.LibraryAccessModeRestricted,
		},
	}}
	service := New(repo, nil, nil)
	tests := []struct {
		name      string
		libraryID string
		accountID string
		profileID string
		master    bool
		want      bool
	}{
		{name: "all", libraryID: "all", accountID: "other", want: true},
		{name: "account grant", libraryID: "restricted", accountID: "account-1", want: true},
		{name: "profile grant", libraryID: "restricted", accountID: "other", profileID: "profile-2", want: true},
		{name: "denied", libraryID: "restricted", accountID: "other", profileID: "other", want: false},
		{name: "master admin no profile", libraryID: "restricted", master: true, want: true},
		{name: "master active profile without grant", libraryID: "restricted", profileID: "other", master: true, want: false},
		{name: "master active profile with grant", libraryID: "restricted", profileID: "profile-2", master: true, want: true},
		{name: "empty restricted master profile", libraryID: "empty-restricted", profileID: "default", master: true, want: true},
		{name: "empty restricted non-master", libraryID: "empty-restricted", accountID: "other", profileID: "profile-1", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.CanAccess(context.Background(), tt.libraryID, tt.accountID, tt.profileID, tt.master)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("CanAccess() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetNormalizesRestrictedPolicy(t *testing.T) {
	repo := &fakeAccessRepo{}
	service := New(repo, nil, nil)
	if err := service.Set(context.Background(), models.LibraryAccessPolicy{
		LibraryID:         " library-1 ",
		AccessMode:        "unexpected",
		AllowedAccountIDs: []string{"account-1", "account-1", ""},
	}); err != nil {
		t.Fatal(err)
	}
	policy := repo.policies["library-1"]
	if policy.AccessMode != models.LibraryAccessModeRestricted || len(policy.AllowedAccountIDs) != 1 {
		t.Fatalf("unexpected normalized policy: %+v", policy)
	}
}

func TestCanAccessStreamAuthorizesOwningLibrary(t *testing.T) {
	repo := &fakeAccessRepo{policies: map[string]models.LibraryAccessPolicy{
		"library-1": {LibraryID: "library-1", AccessMode: models.LibraryAccessModeRestricted, AllowedAccountIDs: []string{"allowed"}},
	}}
	service := New(repo, fakeLocalItems{item: &models.LocalMediaItem{ID: "item-1", LibraryID: "library-1"}}, nil)
	recognized, allowed, err := service.CanAccessStream(context.Background(), "localmedia:item-1/Movie.mkv", "denied", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !recognized || allowed {
		t.Fatalf("recognized=%v allowed=%v, want true/false", recognized, allowed)
	}
	_, allowed, err = service.CanAccessStream(context.Background(), "localmedia:item-1/Movie.mkv", "allowed", "", false)
	if err != nil || !allowed {
		t.Fatalf("allowed account: allowed=%v err=%v", allowed, err)
	}
}
