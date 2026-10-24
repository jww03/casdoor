// Copyright 2025 The Casdoor Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// The permission check samples the directory instead of walking all of it
const (
	larkCheckMaxDepartments = 50
	larkCheckMaxUsers       = 100
	larkCheckMaxRequests    = 60
	larkCheckMaxDuration    = 20 * time.Second
)

const larkScopeEmployee = "contact:user.employee:readonly"

// LarkFieldCheck tells whether Feishu returned a field the syncer reads. Feishu leaves a field
// out of the response when the app lacks its permission, while a permitted but empty field is
// returned empty.
type LarkFieldCheck struct {
	Field string `json:"field"`
	Label string `json:"label"`
	Scope string `json:"scope"`
	// PerUser fields are also left out for users who have no value
	PerUser  bool   `json:"perUser"`
	Note     string `json:"note"`
	Returned bool   `json:"returned"`
}

// Scopes from the "字段权限要求" of the find_by_department and department children APIs. Every
// field but user_id, email and mobile is also covered by contact:contact:readonly_as_app.
var larkCheckedUserFields = []LarkFieldCheck{
	{Field: "user_id", Label: "user ID", Scope: "contact:user.employee_id:readonly"},
	{Field: "email", Label: "email", Scope: "contact:user.email:readonly"},
	{Field: "enterprise_email", Label: "enterprise email", Scope: larkScopeEmployee, PerUser: true, Note: "only returned when Feishu Mail is enabled"},
	{Field: "mobile", Label: "mobile", Scope: "contact:user.phone:readonly"},
	{Field: "gender", Label: "gender", Scope: "contact:user.gender:readonly"},
	{Field: "avatar", Label: "avatar", Scope: "contact:user.base:readonly"},
	{Field: "job_title", Label: "job title", Scope: larkScopeEmployee},
	{Field: "department_ids", Label: "department IDs", Scope: "contact:user.department:readonly"},
	{Field: "status", Label: "status", Scope: larkScopeEmployee},
	{Field: "employee_no", Label: "employee number", Scope: "contact:user.employee_number:read or " + larkScopeEmployee},
	{Field: "join_time", Label: "join time", Scope: larkScopeEmployee},
	{Field: "city", Label: "city", Scope: larkScopeEmployee},
	{Field: "country", Label: "country", Scope: larkScopeEmployee},
	{Field: "leader_user_id", Label: "leader", Scope: "contact:user.department:readonly"},
	{Field: "en_name", Label: "English name", Scope: "contact:user.base:readonly"},
	{Field: "nickname", Label: "nickname", Scope: "contact:user.base:readonly", PerUser: true, Note: "left out for users without one"},
	{Field: "description", Label: "description", Scope: "see Feishu field permissions"},
	{Field: "employee_type", Label: "employee type", Scope: larkScopeEmployee},
	{Field: "is_tenant_manager", Label: "tenant manager flag", Scope: larkScopeEmployee},
	{Field: "custom_attrs", Label: "custom attributes", Scope: larkScopeEmployee, Note: "custom fields must also allow Open Platform API access in the admin console"},
}

var larkCheckedDepartmentName = LarkFieldCheck{Field: "department.name", Label: "department name", Scope: "contact:department.base:readonly"}

// LarkPermissionReport is the result of checking what a Lark app can read from the directory
type LarkPermissionReport struct {
	Host              string            `json:"host"`
	RootDepartmentIds []string          `json:"rootDepartmentIds"`
	Departments       int               `json:"departments"`
	Users             int               `json:"users"`
	Capped            bool              `json:"capped"`
	OverBudget        bool              `json:"overBudget"`
	Fields            []*LarkFieldCheck `json:"fields"`
	// Errors make a sync fail or sync nothing; Warnings do not
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

type larkRawListResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Items []map[string]json.RawMessage `json:"items"`
	} `json:"data"`
}

func getLarkRawString(item map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(item[key], &value)
	return value
}

// CheckLarkSyncerPermissions checks which directory fields the Lark app of a syncer can read. Only
// a failure to get the app token is returned as an error; the other problems are in the report.
func CheckLarkSyncerPermissions(syncer *Syncer) (*LarkPermissionReport, error) {
	return (&LarkSyncerProvider{Syncer: syncer}).checkLarkPermissions()
}

func (p *LarkSyncerProvider) checkLarkPermissions() (*LarkPermissionReport, error) {
	accessToken, err := p.getLarkAccessToken()
	if err != nil {
		return nil, err
	}

	report := &LarkPermissionReport{
		Host:              p.getLarkDomain(),
		RootDepartmentIds: parseLarkRootDepartmentIds(p.Syncer.RootDepartmentIds),
	}
	deadline := time.Now().Add(larkCheckMaxDuration)
	requests := 1
	hasBudget := func() bool {
		if requests >= larkCheckMaxRequests || time.Now().After(deadline) {
			report.OverBudget = true
			return false
		}
		requests++
		return true
	}

	deptName := larkCheckedDepartmentName
	visited := map[string]bool{}
	queue := []string{}
	for _, rootId := range report.RootDepartmentIds {
		if rootId == "0" {
			queue = append(queue, "0")
			continue
		}
		// Every root is resolved whatever the budget, since a wrong one fails the sync
		requests++
		root, err := p.getLarkRootDepartment(accessToken, rootId)
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		if !visited[root.OpenDepartmentId] {
			visited[root.OpenDepartmentId] = true
			report.Departments++
			deptName.Returned = deptName.Returned || root.Name != ""
			queue = append(queue, root.OpenDepartmentId)
		}
	}

	fields := make([]*LarkFieldCheck, len(larkCheckedUserFields))
	for i := range larkCheckedUserFields {
		field := larkCheckedUserFields[i]
		fields[i] = &field
	}
	seenUsers := map[string]bool{}

	// Members and child departments are listed level by level. Past the department cap the walk
	// goes on only while no user has been found, since the departments seen may all be empty.
	for len(queue) > 0 {
		deptId := queue[0]
		queue = queue[1:]

		if report.Users < larkCheckMaxUsers {
			if !hasBudget() {
				break
			}
			items, err := p.getLarkRawPage(accessToken, "/open-apis/contact/v3/users/find_by_department", url.Values{
				"department_id": {deptId},
			}, fmt.Sprintf("failed to get users from dept %s", deptId))
			if err != nil {
				report.Errors = append(report.Errors, err.Error())
			}
			for _, item := range items {
				openId := getLarkRawString(item, "open_id")
				if openId == "" || seenUsers[openId] {
					continue
				}
				if report.Users >= larkCheckMaxUsers {
					report.Capped = true
					break
				}
				seenUsers[openId] = true
				report.Users++
				for _, field := range fields {
					if _, ok := item[field.Field]; ok {
						field.Returned = true
					}
				}
			}
		} else {
			report.Capped = true
		}

		if report.Departments >= larkCheckMaxDepartments && report.Users > 0 {
			report.Capped = true
			if report.Users >= larkCheckMaxUsers {
				break
			}
			continue
		}
		if !hasBudget() {
			break
		}
		items, err := p.getLarkRawPage(accessToken, "/open-apis/contact/v3/departments", url.Values{
			"parent_department_id": {deptId},
			"fetch_child":          {"false"},
		}, fmt.Sprintf("failed to get the child departments of %s", deptId))
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		for _, item := range items {
			childId := getLarkRawString(item, "open_department_id")
			if childId == "" || visited[childId] {
				continue
			}
			if report.Departments >= larkCheckMaxDepartments && report.Users > 0 {
				report.Capped = true
				break
			}
			visited[childId] = true
			report.Departments++
			_, ok := item["name"]
			deptName.Returned = deptName.Returned || ok
			queue = append(queue, childId)
		}
	}

	if report.Users > 0 {
		report.Fields = fields
	}
	if report.Departments > 0 {
		report.Fields = append(report.Fields, &deptName)
	} else if report.Users == 0 {
		report.Warnings = append(report.Warnings, "listing the visible departments returned nothing, the app's contact scope may be empty")
	}
	if report.Users == 0 && len(report.Errors) == 0 {
		if report.OverBudget {
			report.Warnings = append(report.Warnings, "no users were found in the departments sampled before the check ran out of time, the fields could not be checked")
		} else {
			report.Errors = append(report.Errors, "no users are visible to the app, please check the app's contact scope and the root department IDs of the syncer")
		}
	}

	return report, nil
}

// getLarkRawPage gets the first page of a Lark listing with the items left undecoded, so that the
// fields Lark left out can be told from the empty ones
func (p *LarkSyncerProvider) getLarkRawPage(accessToken string, path string, query url.Values, action string) ([]map[string]json.RawMessage, error) {
	query.Set("department_id_type", "open_department_id")
	query.Set("user_id_type", "open_id")
	query.Set("page_size", "50")
	data, err := p.getWithAuth(p.getLarkDomain()+path+"?"+query.Encode(), accessToken)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}

	var resp larkRawListResp
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	if resp.Code != 0 {
		return nil, newLarkDepartmentError(action, resp.Code, resp.Msg)
	}
	return resp.Data.Items, nil
}

func (r *LarkPermissionReport) missingFields() (missing []*LarkFieldCheck, unset []*LarkFieldCheck) {
	for _, field := range r.Fields {
		if field.Returned {
			continue
		}
		if field.PerUser {
			unset = append(unset, field)
		} else {
			missing = append(missing, field)
		}
	}
	return missing, unset
}

// Summary returns the warnings of the report in a few lines for the Test connection message, ""
// when there is nothing to report
func (r *LarkPermissionReport) Summary() string {
	parts := []string{}
	missing, unset := r.missingFields()
	if len(missing) > 0 {
		names := []string{}
		for _, field := range missing {
			names = append(names, fmt.Sprintf("%s (%s)", field.Label, field.Scope))
		}
		parts = append(parts, "Missing Feishu permissions: "+strings.Join(names, ", "))
	}
	if len(unset) > 0 {
		names := []string{}
		for _, field := range unset {
			names = append(names, fmt.Sprintf("%s (%s; %s)", field.Label, field.Note, field.Scope))
		}
		parts = append(parts, "Not returned for any sampled user: "+strings.Join(names, ", "))
	}
	parts = append(parts, r.Warnings...)
	if len(parts) == 0 {
		return ""
	}

	sample := fmt.Sprintf("checked %d departments and %d users", r.Departments, r.Users)
	if r.OverBudget {
		sample += ", cut short by the time or request budget"
	} else if r.Capped {
		sample += ", sample capped"
	}
	return strings.Join(parts, ". ") + " (" + sample + ")"
}

// String returns the whole report as plain text
func (r *LarkPermissionReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Host: %s\n", r.Host)
	fmt.Fprintf(&b, "Root departments: %s\n", strings.Join(r.RootDepartmentIds, ", "))
	fmt.Fprintf(&b, "Departments seen: %d, users seen: %d", r.Departments, r.Users)
	if r.Capped {
		fmt.Fprintf(&b, " (sample capped at %d departments and %d users)", larkCheckMaxDepartments, larkCheckMaxUsers)
	}
	if r.OverBudget {
		fmt.Fprintf(&b, " (cut short by the budget of %d requests or %s)", larkCheckMaxRequests, larkCheckMaxDuration)
	}
	b.WriteString("\n\nFields:\n")
	if len(r.Fields) == 0 {
		b.WriteString("  (no users or departments to check)\n")
	}
	for _, field := range r.Fields {
		status := "ok"
		if !field.Returned {
			needs := "needs " + field.Scope
			if strings.HasPrefix(field.Scope, "see ") {
				needs = field.Scope
			}
			status = "MISSING, " + needs
			if field.PerUser {
				status = "not returned for any sampled user, " + needs
			}
			if field.Note != "" {
				status += " (" + field.Note + ")"
			}
		}
		fmt.Fprintf(&b, "  %-18s %s\n", field.Field, status)
	}
	if len(r.Fields) > 0 {
		b.WriteString("\nEvery scope above but those of user_id, email and mobile is also covered by contact:contact:readonly_as_app.\n")
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(&b, "\nWarning: %s\n", warning)
	}
	for _, problem := range r.Errors {
		fmt.Fprintf(&b, "\nError: %s\n", problem)
	}
	return b.String()
}

// TestConnectionWithWarnings checks the app token, the root departments and the directory fields
// the app can read. Missing optional fields come back as a warning message instead of an error.
func (p *LarkSyncerProvider) TestConnectionWithWarnings() (string, error) {
	report, err := p.checkLarkPermissions()
	if err != nil {
		return "", err
	}
	if len(report.Errors) > 0 {
		return "", errors.New(strings.Join(slices.Concat(report.Errors, report.Warnings), "; "))
	}
	return report.Summary(), nil
}
