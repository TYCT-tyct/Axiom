package declared

import (
	"errors"
	"fmt"

	"axiom/internal/suppliers/internal/adapter"
)

func (c *compiler) keys(spec Keys) (keysPlan, error) {
	var k keysPlan
	var err error
	if k.list, err = c.keyList(spec.List); err != nil {
		return k, err
	}
	if k.secret, err = c.keySecret(spec.Secret); err != nil {
		return k, err
	}
	if spec.Create != nil {
		if k.create, err = c.keyCreate(*spec.Create); err != nil {
			return k, err
		}
	}
	switch {
	case spec.Unrestricted && spec.Policy != nil:
		return k, errors.New("keys: declare unrestricted or policy, not both")
	case !spec.Unrestricted && spec.Policy == nil:
		return k, errors.New("keys: declare unrestricted, or the policy fields a listing row holds")
	case spec.Unrestricted:
		k.unrestricted = true
	default:
		if k.policy, err = c.keyPolicy(*spec.Policy); err != nil {
			return k, err
		}
	}
	return k, nil
}

func (c *compiler) keyPolicy(spec KeyPolicy) (*keyPolicyPlan, error) {
	p := &keyPolicyPlan{}
	var err error
	if p.models, err = c.path("keys.policy.models", spec.Models); err != nil {
		return nil, err
	}
	if p.expiresAt, err = c.optionalPath("keys.policy.expires_at", spec.ExpiresAt); err != nil {
		return nil, err
	}
	return p, nil
}

func (c *compiler) keyList(spec KeyList) (keyListPlan, error) {
	var k keyListPlan
	var err error
	if k.path, err = c.pathTemplate("keys.list.path", spec.Path, "page", "size"); err != nil {
		return k, err
	}
	if k.pageSize, err = keyPaging(k.path, spec.PageSize, spec.Total); err != nil {
		return k, err
	}
	if err := c.keyListFields(&k, spec); err != nil {
		return k, err
	}
	if k.statuses, err = keyStatusMap(spec.Statuses, k.status); err != nil {
		return k, err
	}
	if k.bucket, err = c.bucket("keys.list", spec.Bucket); err != nil {
		return k, err
	}
	return k, nil
}

func keyPaging(path tmpl, pageSize int, total string) (int, error) {
	page, size := path.has("page"), path.has("size")
	if page != size {
		return 0, errors.New("keys.list.path: {page} and {size} go together")
	}
	if !page {
		if pageSize != 0 || total != "" {
			return 0, errors.New("keys.list: page_size and total are only for a path with {page} and {size}")
		}
		return 0, nil
	}
	if pageSize <= 0 || pageSize > 1000 {
		return 0, fmt.Errorf("keys.list.page_size: %d, want 1 to 1000", pageSize)
	}
	return pageSize, nil
}

func (c *compiler) keyListFields(k *keyListPlan, spec KeyList) error {
	var err error
	if k.total, err = c.optionalPath("keys.list.total", spec.Total); err != nil {
		return err
	}
	if k.items, err = c.path("keys.list.items", spec.Items); err != nil {
		return err
	}
	if k.ref, err = c.path("keys.list.ref", spec.Ref); err != nil {
		return err
	}
	if k.group, err = c.optionalPath("keys.list.group", spec.Group); err != nil {
		return err
	}
	if k.name, err = c.optionalPath("keys.list.name", spec.Name); err != nil {
		return err
	}
	if k.status, err = c.optionalPath("keys.list.status", spec.Status); err != nil {
		return err
	}
	if k.resetAt, err = c.optionalPath("keys.list.reset_at", spec.ResetAt); err != nil {
		return err
	}
	if k.resetAt != nil && k.status == nil {
		return errors.New("keys.list.reset_at: a status is not read")
	}
	return nil
}

func keyStatusMap(spec map[string]string, status *path) (map[string]adapter.KeyStatus, error) {
	if len(spec) > 0 && status == nil {
		return nil, errors.New("keys.list.statuses: a status is not read")
	}
	out := make(map[string]adapter.KeyStatus, len(spec))
	for from, to := range spec {
		if from == "" {
			return nil, errors.New("keys.list.statuses: an empty value")
		}
		s, ok := keyStatuses[to]
		if !ok {
			return nil, fmt.Errorf("keys.list.statuses: %q is not active, disabled, quota_exhausted or expired", to)
		}
		out[from] = s
	}
	return out, nil
}

func (c *compiler) keySecret(spec KeySecret) (keySecretPlan, error) {
	var k keySecretPlan
	var err error
	if k.path, err = c.pathTemplate("keys.secret.path", spec.Path, "ref"); err != nil {
		return k, err
	}
	if !k.path.has("ref") {
		return k, errors.New("keys.secret.path: must name {ref}, the key whose text is read")
	}
	if k.token, err = c.path("keys.secret.token", spec.Token); err != nil {
		return k, err
	}
	if k.bucket, err = c.bucket("keys.secret", spec.Bucket); err != nil {
		return k, err
	}
	return k, nil
}

func (c *compiler) keyCreate(spec KeyCreate) (*keyCreatePlan, error) {
	k := &keyCreatePlan{}
	var err error
	if k.path, err = c.pathTemplate("keys.create.path", spec.Path); err != nil {
		return nil, err
	}
	if k.body, err = c.keyCreateBody(spec.Body); err != nil {
		return nil, err
	}
	if k.ref, err = c.path("keys.create.ref", spec.Ref); err != nil {
		return nil, err
	}
	if k.bucket, err = c.bucket("keys.create", spec.Bucket); err != nil {
		return nil, err
	}
	return k, nil
}

func (c *compiler) keyCreateBody(spec map[string]string) (map[string]tmpl, error) {
	body := make(map[string]tmpl, len(spec))
	named := false
	for member, text := range spec {
		if !memberName(member) {
			return nil, fmt.Errorf("keys.create.body: %q is not a member name", member)
		}
		t, err := c.template("keys.create.body."+member, text, "name", "group")
		if err != nil {
			return nil, err
		}
		for _, p := range t.parts {
			named = named || p.variable == "name"
		}
		body[member] = t
	}
	if !named {
		return nil, errors.New("keys.create.body: the key's name must be sent, so a listing can adopt a key whose answer was lost")
	}
	return body, nil
}
