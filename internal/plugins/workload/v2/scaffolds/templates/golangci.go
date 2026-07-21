// Copyright 2024 Nukleros
// SPDX-License-Identifier: Apache-2.0

/*
Copyright 2022 The Kubernetes Authors.

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

package templates

import (
	"sigs.k8s.io/kubebuilder/v4/pkg/machinery"

	commontemplates "github.com/nukleros/operator-builder/internal/plugins/workload/templates"
)

var _ machinery.Template = &Golangci{}

// Golangci scaffolds a file which define Golangci rules.
type Golangci struct {
	machinery.TemplateMixin
	machinery.ProjectNameMixin

	// Overwrite causes the file to be overwritten if it already exists.
	// By default the file is skipped to preserve custom lint rules.
	Overwrite bool
}

// SetTemplateDefaults implements machinery.Template.
func (f *Golangci) SetTemplateDefaults() error {
	if f.Path == "" {
		f.Path = ".golangci.yml"
	}

	f.TemplateBody = commontemplates.Linter

	if f.Overwrite {
		f.IfExistsAction = machinery.OverwriteFile
	} else {
		f.IfExistsAction = machinery.SkipFile
	}

	return nil
}
