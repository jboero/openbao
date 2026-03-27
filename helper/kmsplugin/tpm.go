// Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
// SPDX-License-Identifier: MPL-2.0

//go:build linux

package kmsplugin

import (
	"github.com/openbao/openbao/helper/seal/tpm"
)

func init() {
	builtinWrappers[tpm.WrapperTypeTPM] = toWrapper(tpm.NewWrapper)
}
