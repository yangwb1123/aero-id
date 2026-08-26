package grpc

import (
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

func updateProfileInput(req *aeropb.UpdateProfileRequest) service.UpdateAccountProfileInput {
	if req == nil {
		return service.UpdateAccountProfileInput{}
	}
	input := service.UpdateAccountProfileInput{
		DisplayName: req.DisplayName,
		AvatarURL:   req.AvatarUrl,
		Locale:      req.Locale,
		Timezone:    req.Timezone,
	}
	if req.Preferences != nil {
		input.Preferences = req.Preferences.AsMap()
	}
	return input
}

func accountViewToProto(view *service.AccountView) (*aeropb.AccountView, error) {
	if view == nil {
		return nil, fmt.Errorf("account view is nil")
	}
	profile, err := accountProfileToProto(view.Profile)
	if err != nil {
		return nil, err
	}
	sources, err := accountSourcesToProto(view.Sources)
	if err != nil {
		return nil, err
	}
	memberships, err := accountMembershipsToProto(view.Memberships)
	if err != nil {
		return nil, err
	}
	return &aeropb.AccountView{
		Account:     accountToProto(view.Account),
		Profile:     profile,
		Sources:     sources,
		Memberships: memberships,
	}, nil
}

func accountsToProto(accounts []*domain.Account) []*aeropb.Account {
	result := make([]*aeropb.Account, 0, len(accounts))
	for _, account := range accounts {
		if account != nil {
			result = append(result, accountToProto(account))
		}
	}
	return result
}

func accountToProto(account *domain.Account) *aeropb.Account {
	if account == nil {
		return nil
	}
	return &aeropb.Account{
		AccountId:    account.ID.String(),
		CanonicalUid: account.CanonicalUID,
		HomeRegion:   account.HomeRegion,
		Status:       string(account.Status),
	}
}

func accountProfileToProto(profile *domain.AccountProfile) (*aeropb.AccountProfile, error) {
	if profile == nil {
		return nil, fmt.Errorf("account profile is nil")
	}
	preferences, err := structFromMap(profile.Preferences)
	if err != nil {
		return nil, apperr.Internal("account.profile_projection_invalid", "account profile projection is invalid").Wrap(err)
	}
	return &aeropb.AccountProfile{
		AccountId:   profile.AccountID.String(),
		DisplayName: profile.DisplayName,
		AvatarUrl:   profile.AvatarURL,
		Locale:      profile.Locale,
		Timezone:    profile.Timezone,
		Preferences: preferences,
	}, nil
}

func accountSourcesToProto(sources []*domain.AccountSource) ([]*aeropb.AccountSource, error) {
	result := make([]*aeropb.AccountSource, 0, len(sources))
	for _, source := range sources {
		if source == nil {
			continue
		}
		item, err := accountSourceToProto(source)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func accountSourceToProto(source *domain.AccountSource) (*aeropb.AccountSource, error) {
	data, err := structFromMap(source.Data)
	if err != nil {
		return nil, apperr.Internal("account.source_projection_invalid", "account source projection is invalid").Wrap(err)
	}
	return &aeropb.AccountSource{
		Id:              source.ID.String(),
		Source:          source.Source,
		SourceRegion:    source.SourceRegion,
		SourceAccountId: source.SourceAccountID,
		ScopeType:       source.ScopeType,
		ScopeId:         source.ScopeID,
		Status:          source.Status,
		Version:         source.Version,
		GeneratedAt:     timestampOf(source.GeneratedAt),
		LastSyncedAt:    timestampOf(source.LastSyncedAt),
		ErrorCode:       source.ErrorCode,
		Data:            data,
	}, nil
}

func accountMembershipsToProto(memberships []*domain.AccountMembership) ([]*aeropb.AccountMembership, error) {
	result := make([]*aeropb.AccountMembership, 0, len(memberships))
	for _, membership := range memberships {
		if membership == nil {
			continue
		}
		data, err := structFromMap(membership.Data)
		if err != nil {
			return nil, apperr.Internal("account.membership_projection_invalid", "account membership projection is invalid").Wrap(err)
		}
		result = append(result, &aeropb.AccountMembership{
			Id:             membership.ID.String(),
			Source:         membership.Source,
			SourceRegion:   membership.SourceRegion,
			ScopeType:      membership.ScopeType,
			ScopeId:        membership.ScopeID,
			SourceMemberId: membership.SourceMemberID,
			Role:           membership.Role,
			Status:         membership.Status,
			Version:        membership.Version,
			LastSyncedAt:   timestampOf(membership.LastSyncedAt),
			Data:           data,
		})
	}
	return result, nil
}

func structFromMap(values map[string]any) (*structpb.Struct, error) {
	if values == nil {
		return nil, nil
	}
	// structpb.NewStruct accepts JSON-shaped []any values, but not typed slices
	// such as []string or []domain.AccountDataset. Payloads created by the
	// service intentionally use typed dataset slices, so normalize through the
	// JSON wire shape before handing the value to protobuf.
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	normalized := make(map[string]any)
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	return structpb.NewStruct(normalized)
}

func timestampOf(value *time.Time) *timestamppb.Timestamp {
	if value == nil || value.IsZero() {
		return nil
	}
	return timestamppb.New(*value)
}

func aggregateViewToProto(view *domain.AggregateOverview) (*aeropb.AggregationView, error) {
	if view == nil {
		return nil, fmt.Errorf("aggregation view is nil")
	}
	profile, err := accountProfileToProto(view.Profile)
	if err != nil {
		return nil, err
	}
	snapshots := make([]*aeropb.AccountSnapshot, 0, len(view.Snapshots))
	for _, snapshot := range view.Snapshots {
		item, err := accountSnapshotToProto(snapshot)
		if err != nil {
			return nil, err
		}
		if item != nil {
			snapshots = append(snapshots, item)
		}
	}
	stale := make([]string, len(view.StaleDatasets))
	for i, dataset := range view.StaleDatasets {
		stale[i] = string(dataset)
	}
	return &aeropb.AggregationView{
		Account:       accountToProto(view.Account),
		Profile:       profile,
		Snapshots:     snapshots,
		Consistency:   string(view.Consistency),
		Partial:       view.Partial,
		SourceErrors:  view.SourceErrors,
		StaleDatasets: stale,
		GeneratedAt:   timestamppb.New(view.GeneratedAt),
	}, nil
}

func accountSnapshotToProto(snapshot *domain.AccountSnapshot) (*aeropb.AccountSnapshot, error) {
	if snapshot == nil {
		return nil, nil
	}
	data, err := structFromMap(snapshot.Data)
	if err != nil {
		return nil, apperr.Internal("aggregation.snapshot_projection_invalid", "aggregation snapshot is invalid").Wrap(err)
	}
	return &aeropb.AccountSnapshot{
		Id:           snapshot.ID.String(),
		Source:       snapshot.Source,
		SourceRegion: snapshot.SourceRegion,
		Dataset:      string(snapshot.Dataset),
		Status:       string(snapshot.Status),
		Version:      snapshot.Version,
		GeneratedAt:  timestamppb.New(snapshot.GeneratedAt),
		LastSyncedAt: timestampOf(snapshot.LastSyncedAt),
		ExpiresAt:    timestampOf(snapshot.ExpiresAt),
		ErrorCode:    snapshot.ErrorCode,
		Data:         data,
	}, nil
}
