package service

import (
	"context"
	"encoding/json"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
)

type SingBoxClientParametersInput struct {
	Parameters   *singbox.ClientParameters `json:"parameters,omitempty"`
	OriginalLink *string                   `json:"original_link,omitempty"`
}

type SingBoxClientParametersResult struct {
	Parameters          *singbox.ClientParameters `json:"parameters"`
	EffectiveParameters singbox.ClientParameters  `json:"effective_parameters"`
}

func (s *Service) managedSingBoxBundle(ctx context.Context, id int64) (*model.SingBoxUser, *singbox.Bundle, error) {
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if u.Source != model.SingBoxManaged {
		return nil, nil, ErrSingBoxNotManaged
	}
	plain, err := s.sealer.Open(u.ConfigBlob)
	if err != nil {
		return nil, nil, singbox.ErrClientExport
	}
	var b singbox.Bundle
	if json.Unmarshal(plain, &b) != nil {
		return nil, nil, singbox.ErrClientExport
	}
	b.Name, b.Port = u.Name, u.Port
	return u, &b, nil
}

func singBoxExportHost(node *model.Node) string {
	if node.IPv6Entry != "" {
		return node.IPv6Entry
	}
	if node.IPv6Address != "" {
		return node.IPv6Address
	}
	return node.Host
}

func chooseClientParameters(bundle *singbox.Bundle, node *model.Node, in SingBoxClientParametersInput) (*singbox.ClientParameters, error) {
	if (in.Parameters == nil) == (in.OriginalLink == nil) {
		return nil, singbox.ErrClientExport
	}
	if in.OriginalLink != nil {
		return bundle.ImportAnyTLSLink(*in.OriginalLink, node.Host, node.IPv6Address, node.IPv6Entry)
	}
	if err := bundle.ValidateClientParameters(*in.Parameters); err != nil {
		return nil, err
	}
	p := *in.Parameters
	return &p, nil
}

func (s *Service) SingBoxClientParameters(ctx context.Context, id int64) (*SingBoxClientParametersResult, error) {
	u, b, err := s.managedSingBoxBundle(ctx, id)
	if err != nil {
		return nil, err
	}
	node, err := s.store.NodeByID(ctx, u.NodeID)
	if err != nil {
		return nil, err
	}
	effective, err := b.EffectiveClientParameters(singBoxExportHost(node))
	if err != nil {
		return nil, err
	}
	return &SingBoxClientParametersResult{Parameters: b.ClientParameters, EffectiveParameters: effective}, nil
}

func (s *Service) SetSingBoxClientParameters(ctx context.Context, id int64, in SingBoxClientParametersInput) error {
	u, b, err := s.managedSingBoxBundle(ctx, id)
	if err != nil {
		return err
	}
	node, err := s.store.NodeByID(ctx, u.NodeID)
	if err != nil {
		return err
	}
	p, err := chooseClientParameters(b, node, in)
	if err != nil {
		return err
	}
	b.ClientParameters = p
	// Fail before saving if any original inbound option cannot be exported.
	if _, _, err := b.ExportClient(singBoxExportHost(node)); err != nil {
		return err
	}
	sealed, err := s.sealer.Seal(mustJSON(b))
	if err != nil {
		return err
	}
	return s.store.CompareAndSwapSingBoxConfig(ctx, id, u.ConfigBlob, sealed)
}

func (s *Service) ResetSingBoxClientParameters(ctx context.Context, id int64) error {
	u, b, err := s.managedSingBoxBundle(ctx, id)
	if err != nil {
		return err
	}
	b.ClientParameters = nil
	sealed, err := s.sealer.Seal(mustJSON(b))
	if err != nil {
		return err
	}
	return s.store.CompareAndSwapSingBoxConfig(ctx, id, u.ConfigBlob, sealed)
}
