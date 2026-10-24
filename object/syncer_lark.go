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
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/casdoor/casdoor/util"
)

// LarkSyncerProvider implements SyncerProvider for Lark API-based syncers
type LarkSyncerProvider struct {
	Syncer *Syncer
}

// InitAdapter initializes the Lark syncer (no database adapter needed)
func (p *LarkSyncerProvider) InitAdapter() error {
	// Lark syncer doesn't need database adapter
	return nil
}

// GetOriginalUsers retrieves all users from Lark API
func (p *LarkSyncerProvider) GetOriginalUsers() ([]*OriginalUser, error) {
	return p.getLarkUsers()
}

// AddUser adds a new user to Lark (not supported for read-only API)
func (p *LarkSyncerProvider) AddUser(user *OriginalUser) (bool, error) {
	// Lark syncer is typically read-only
	return false, fmt.Errorf("adding users to Lark is not supported")
}

// UpdateUser updates an existing user in Lark (not supported for read-only API)
func (p *LarkSyncerProvider) UpdateUser(user *OriginalUser) (bool, error) {
	// Lark syncer is typically read-only
	return false, fmt.Errorf("updating users in Lark is not supported")
}

// TestConnection tests the Lark API connection, the root departments of the syncer and that
// some users are visible; see TestConnectionWithWarnings
func (p *LarkSyncerProvider) TestConnection() error {
	_, err := p.TestConnectionWithWarnings()
	return err
}

// Close closes any open connections (no-op for Lark API-based syncer)
func (p *LarkSyncerProvider) Close() error {
	// Lark syncer doesn't maintain persistent connections
	return nil
}

type LarkAccessTokenResp struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int    `json:"expire"`
}

type LarkUser struct {
	UserId          string      `json:"user_id"`
	UnionId         string      `json:"union_id"`
	OpenId          string      `json:"open_id"`
	Name            string      `json:"name"`
	EnName          string      `json:"en_name"`
	Email           string      `json:"email"`
	EnterpriseEmail string      `json:"enterprise_email"`
	Mobile          string      `json:"mobile"`
	Gender          int         `json:"gender"`
	Avatar          *LarkAvatar `json:"avatar"`
	Status          *LarkStatus `json:"status"`
	DepartmentIds   []string    `json:"department_ids"`
	JobTitle        string      `json:"job_title"`

	Nickname        larkLooseString `json:"nickname"`
	Description     larkLooseString `json:"description"`
	EmployeeNo      larkLooseString `json:"employee_no"`
	JoinTime        larkLooseInt    `json:"join_time"`
	City            larkLooseString `json:"city"`
	Country         larkLooseString `json:"country"`
	LeaderUserId    larkLooseString `json:"leader_user_id"`
	EmployeeType    larkLooseInt    `json:"employee_type"`
	IsTenantManager *larkLooseBool  `json:"is_tenant_manager"`
	CustomAttrs     larkCustomAttrs `json:"custom_attrs"`
}

// LarkCustomAttr is a custom field of a Lark user; which value field is set depends on the type
type LarkCustomAttr struct {
	Type  string `json:"type"`
	Id    string `json:"id"`
	Value struct {
		Text        string `json:"text"`
		Url         string `json:"url"`
		PcUrl       string `json:"pc_url"`
		OptionId    string `json:"option_id"`
		OptionValue string `json:"option_value"`
		Name        string `json:"name"`
		PictureUrl  string `json:"picture_url"`
		GenericUser *struct {
			Id string `json:"id"`
		} `json:"generic_user"`
	} `json:"value"`
}

// The loose types below decode the optional profile fields so that a value of an unexpected
// type is dropped instead of failing the whole user listing.
type (
	larkLooseString string
	larkLooseInt    int64
	larkLooseBool   bool
	larkCustomAttrs []*LarkCustomAttr
)

func decodeLarkLooseValue(data []byte) interface{} {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value interface{}
	if decoder.Decode(&value) != nil {
		return nil
	}
	return value
}

func (s *larkLooseString) UnmarshalJSON(data []byte) error {
	switch value := decodeLarkLooseValue(data).(type) {
	case string:
		*s = larkLooseString(value)
	case json.Number:
		*s = larkLooseString(value.String())
	default:
		*s = ""
	}
	return nil
}

func (i *larkLooseInt) UnmarshalJSON(data []byte) error {
	*i = 0
	var number json.Number
	switch value := decodeLarkLooseValue(data).(type) {
	case json.Number:
		number = value
	case string:
		number = json.Number(strings.TrimSpace(value))
	}
	if n, err := number.Int64(); err == nil {
		*i = larkLooseInt(n)
	}
	return nil
}

func (b *larkLooseBool) UnmarshalJSON(data []byte) error {
	switch value := decodeLarkLooseValue(data).(type) {
	case bool:
		*b = larkLooseBool(value)
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
		*b = larkLooseBool(parsed)
	default:
		*b = false
	}
	return nil
}

func (a *larkCustomAttrs) UnmarshalJSON(data []byte) error {
	*a = nil
	var items []json.RawMessage
	if json.Unmarshal(data, &items) != nil {
		return nil
	}
	for _, item := range items {
		attr := &LarkCustomAttr{}
		if json.Unmarshal(item, attr) == nil && attr.Id != "" {
			*a = append(*a, attr)
		}
	}
	return nil
}

type LarkAvatar struct {
	Avatar72     string `json:"avatar_72"`
	Avatar240    string `json:"avatar_240"`
	Avatar640    string `json:"avatar_640"`
	AvatarOrigin string `json:"avatar_origin"`
}

type LarkStatus struct {
	IsFrozen    bool `json:"is_frozen"`
	IsResigned  bool `json:"is_resigned"`
	IsActivated bool `json:"is_activated"`
	IsExited    bool `json:"is_exited"`
}

type LarkUserListResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Items     []*LarkUser `json:"items"`
		HasMore   bool        `json:"has_more"`
		PageToken string      `json:"page_token"`
	} `json:"data"`
}

// LarkDepartment holds the department fields the syncer reads. parent_department_id is not
// read: Lark leaves it empty unless the app may read the organization structure.
type LarkDepartment struct {
	Name             string `json:"name"`
	DepartmentId     string `json:"department_id"`
	OpenDepartmentId string `json:"open_department_id"`
}

type LarkDeptListResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Items     []*LarkDepartment `json:"items"`
		HasMore   bool              `json:"has_more"`
		PageToken string            `json:"page_token"`
	} `json:"data"`
}

type LarkDeptGetResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Department *LarkDepartment `json:"department"`
	} `json:"data"`
}

// larkDepartments is the part of the Lark directory a syncer covers.
type larkDepartments struct {
	// wholeCompany is set for the root "0", which has no group but whose direct members are synced
	wholeCompany bool
	list         []*LarkDepartment
	// parents maps each covered open_department_id to its parent's, "" for a top group
	parents map[string]string
}

func (d *larkDepartments) add(dept *LarkDepartment, parentId string) {
	d.list = append(d.list, dept)
	d.parents[dept.OpenDepartmentId] = parentId
}

func (d *larkDepartments) contains(deptId string) bool {
	_, ok := d.parents[deptId]
	return ok
}

// larkDepartmentSnapshot is the department tree walked by GetOriginalGroups, kept on the syncer
// so that GetOriginalUsers right after it in the same run does not walk the tree again
type larkDepartmentSnapshot struct {
	rootIds   string
	createdAt time.Time
	depts     *larkDepartments
}

const larkDepartmentSnapshotTTL = 5 * time.Minute

// userDepartmentIds returns the departments whose direct members are listed
func (d *larkDepartments) userDepartmentIds() []string {
	deptIds := []string{}
	if d.wholeCompany {
		deptIds = append(deptIds, "0")
	}
	for _, dept := range d.list {
		deptIds = append(deptIds, dept.OpenDepartmentId)
	}
	return deptIds
}

// Lark answers these codes for departments outside the app's contact scope
const (
	larkCodeNoDeptAuthority       = 40004
	larkCodeNoParentDeptAuthority = 40014
)

func newLarkDepartmentError(action string, code int, msg string) error {
	if code == larkCodeNoDeptAuthority || code == larkCodeNoParentDeptAuthority {
		return fmt.Errorf("%s: the app cannot see these departments, please check the app's contact scope or the root department IDs of the syncer: code=%d, msg=%s", action, code, msg)
	}
	return fmt.Errorf("%s: code=%d, msg=%s", action, code, msg)
}

// getLarkDomain returns the Lark API domain based on whether global endpoint is used
func (p *LarkSyncerProvider) getLarkDomain() string {
	// syncer.Host overrides the endpoint, e.g. https://open.feishu.cn for Feishu (China).
	// If empty, default to global endpoint (larksuite.com)
	host := strings.TrimRight(strings.TrimSpace(p.Syncer.Host), "/")
	if host != "" {
		return host
	}
	return "https://open.larksuite.com"
}

// parseLarkRootDepartmentIds splits the root department IDs of the syncer. No IDs, or "0"
// among them, means the whole company.
func parseLarkRootDepartmentIds(value string) []string {
	deptIds := []string{}
	seen := map[string]bool{}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || unicode.IsSpace(r)
	})
	for _, deptId := range fields {
		if deptId == "0" {
			return []string{"0"}
		}
		if !seen[deptId] {
			seen[deptId] = true
			deptIds = append(deptIds, deptId)
		}
	}
	if len(deptIds) == 0 {
		return []string{"0"}
	}
	return deptIds
}

// getLarkAccessToken gets access token from Lark API
func (p *LarkSyncerProvider) getLarkAccessToken() (string, error) {
	// syncer.User should be the app_id
	// syncer.Password should be the app_secret
	appId := p.Syncer.User
	if appId == "" {
		return "", fmt.Errorf("app_id (user field) is required for Lark syncer")
	}

	appSecret := p.Syncer.Password
	if appSecret == "" {
		return "", fmt.Errorf("app_secret (password field) is required for Lark syncer")
	}

	domain := p.getLarkDomain()
	apiUrl := fmt.Sprintf("%s/open-apis/auth/v3/tenant_access_token/internal", domain)

	postData := map[string]string{
		"app_id":     appId,
		"app_secret": appSecret,
	}

	data, err := p.postJSON(apiUrl, postData)
	if err != nil {
		return "", err
	}

	var tokenResp LarkAccessTokenResp
	err = json.Unmarshal(data, &tokenResp)
	if err != nil {
		return "", err
	}

	if tokenResp.Code != 0 {
		return "", fmt.Errorf("failed to get access token: code=%d, msg=%s",
			tokenResp.Code, tokenResp.Msg)
	}

	return tokenResp.TenantAccessToken, nil
}

// larkMaxPages bounds a paginated listing so a misbehaving API cannot keep the syncer busy forever.
const larkMaxPages = 10000

// checkLarkPageToken validates the cursor of a page that reported has_more and records it in seen.
func checkLarkPageToken(seen map[string]bool, pageToken string, page int, listing string) error {
	if pageToken == "" {
		return fmt.Errorf("Lark %s listing reported has_more without a page_token on page %d", listing, page)
	}
	if seen[pageToken] {
		return fmt.Errorf("Lark %s listing repeated a page_token on page %d", listing, page)
	}
	if page >= larkMaxPages {
		return fmt.Errorf("Lark %s listing exceeded %d pages", listing, larkMaxPages)
	}
	seen[pageToken] = true
	return nil
}

// getLarkDepartments gets the departments under the syncer's root departments
func (p *LarkSyncerProvider) getLarkDepartments(accessToken string) (*larkDepartments, error) {
	depts := &larkDepartments{parents: map[string]string{}}
	for _, rootId := range parseLarkRootDepartmentIds(p.Syncer.RootDepartmentIds) {
		if rootId == "0" {
			depts.wholeCompany = true
		} else {
			root, err := p.getLarkRootDepartment(accessToken, rootId)
			if err != nil {
				return nil, err
			}
			// Already reached from another root, together with its subtree
			if depts.contains(root.OpenDepartmentId) {
				continue
			}
			depts.add(root, "")
			rootId = root.OpenDepartmentId
		}

		err := p.walkLarkDepartments(accessToken, rootId, depts)
		if err != nil {
			return nil, err
		}
	}

	return depts, nil
}

// getLarkRootIdsKey identifies the root list a department snapshot was built for
func (p *LarkSyncerProvider) getLarkRootIdsKey() string {
	return strings.Join(parseLarkRootDepartmentIds(p.Syncer.RootDepartmentIds), ",")
}

// takeLarkDepartmentSnapshot returns the departments walked by the group sync of this run, if they
// are recent and were built for the current roots, and clears them so that no later run reuses them
func (p *LarkSyncerProvider) takeLarkDepartmentSnapshot() *larkDepartments {
	snapshot := p.Syncer.larkDepartments
	p.Syncer.larkDepartments = nil
	if snapshot == nil || snapshot.rootIds != p.getLarkRootIdsKey() || time.Since(snapshot.createdAt) > larkDepartmentSnapshotTTL {
		return nil
	}
	return snapshot.depts
}

// getLarkRootDepartment looks up a root department given either by open_department_id ("od-")
// or by custom department_id, so that the rest of the sync only deals with open IDs.
func (p *LarkSyncerProvider) getLarkRootDepartment(accessToken string, deptId string) (*LarkDepartment, error) {
	deptIdType := "department_id"
	if strings.HasPrefix(deptId, "od-") {
		deptIdType = "open_department_id"
	}
	query := url.Values{
		"department_id_type": {deptIdType},
		"user_id_type":       {"open_id"},
	}
	apiUrl := p.getLarkDomain() + "/open-apis/contact/v3/departments/" + url.PathEscape(deptId) + "?" + query.Encode()

	data, err := p.getWithAuth(apiUrl, accessToken)
	if err != nil {
		return nil, err
	}

	var deptResp LarkDeptGetResp
	err = json.Unmarshal(data, &deptResp)
	if err != nil {
		return nil, err
	}

	if deptResp.Code != 0 {
		return nil, newLarkDepartmentError(fmt.Sprintf("failed to get root department %s", deptId), deptResp.Code, deptResp.Msg)
	}

	dept := deptResp.Data.Department
	if dept == nil || dept.OpenDepartmentId == "" {
		return nil, fmt.Errorf("Lark returned no open_department_id for root department %s", deptId)
	}

	return dept, nil
}

// walkLarkDepartments adds the departments below rootId to depts, level by level. Each
// department's parent is the department whose children listing returned it.
func (p *LarkSyncerProvider) walkLarkDepartments(accessToken string, rootId string, depts *larkDepartments) error {
	visited := map[string]bool{rootId: true}
	queue := []string{rootId}
	for len(queue) > 0 {
		parentId := queue[0]
		queue = queue[1:]

		children, err := p.getLarkChildDepartments(accessToken, parentId)
		if err != nil {
			return err
		}

		groupParentId := parentId
		if parentId == "0" {
			groupParentId = ""
		}
		for _, child := range children {
			childId := child.OpenDepartmentId
			if visited[childId] {
				continue
			}
			visited[childId] = true

			if parent, ok := depts.parents[childId]; ok {
				// A root walked earlier that lies inside this root: its subtree is already
				// there, it only stops being a top group.
				if parent == "" {
					depts.parents[childId] = groupParentId
				}
				continue
			}

			depts.add(child, groupParentId)
			queue = append(queue, childId)
		}
	}

	return nil
}

// getLarkChildDepartments gets the direct child departments of a department
func (p *LarkSyncerProvider) getLarkChildDepartments(accessToken string, parentId string) ([]*LarkDepartment, error) {
	domain := p.getLarkDomain()
	allDepts := []*LarkDepartment{}
	pageToken := ""
	seenTokens := map[string]bool{}

	for page := 1; ; page++ {
		query := url.Values{
			"parent_department_id": {parentId},
			"department_id_type":   {"open_department_id"},
			"user_id_type":         {"open_id"},
			"fetch_child":          {"false"},
			"page_size":            {"50"},
		}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		apiUrl := domain + "/open-apis/contact/v3/departments?" + query.Encode()

		data, err := p.getWithAuth(apiUrl, accessToken)
		if err != nil {
			return nil, err
		}

		var deptResp LarkDeptListResp
		err = json.Unmarshal(data, &deptResp)
		if err != nil {
			return nil, err
		}

		if deptResp.Code != 0 {
			return nil, newLarkDepartmentError(fmt.Sprintf("failed to get the child departments of %s", parentId), deptResp.Code, deptResp.Msg)
		}

		for _, dept := range deptResp.Data.Items {
			// A missing department would drop its members, and the syncer would forbid them
			if dept == nil || dept.OpenDepartmentId == "" {
				return nil, fmt.Errorf("Lark child department of %s is missing open_department_id; refusing an incomplete department snapshot", parentId)
			}
			allDepts = append(allDepts, dept)
		}

		if !deptResp.Data.HasMore {
			break
		}
		pageToken = deptResp.Data.PageToken
		err = checkLarkPageToken(seenTokens, pageToken, page, fmt.Sprintf("department %s child", parentId))
		if err != nil {
			return nil, err
		}
	}

	return allDepts, nil
}

// getLarkUsersFromDept gets users from a specific department
func (p *LarkSyncerProvider) getLarkUsersFromDept(accessToken string, deptId string) ([]*LarkUser, error) {
	domain := p.getLarkDomain()
	allUsers := []*LarkUser{}
	pageToken := ""
	seenTokens := map[string]bool{}

	for page := 1; ; page++ {
		query := url.Values{
			"department_id":      {deptId},
			"department_id_type": {"open_department_id"},
			"user_id_type":       {"open_id"},
			"page_size":          {"50"},
		}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		apiUrl := domain + "/open-apis/contact/v3/users/find_by_department?" + query.Encode()

		data, err := p.getWithAuth(apiUrl, accessToken)
		if err != nil {
			return nil, err
		}

		var userResp LarkUserListResp
		err = json.Unmarshal(data, &userResp)
		if err != nil {
			return nil, err
		}

		if userResp.Code != 0 {
			return nil, fmt.Errorf("failed to get users from dept %s: code=%d, msg=%s",
				deptId, userResp.Code, userResp.Msg)
		}

		allUsers = append(allUsers, userResp.Data.Items...)

		if !userResp.Data.HasMore {
			break
		}
		pageToken = userResp.Data.PageToken
		err = checkLarkPageToken(seenTokens, pageToken, page, fmt.Sprintf("department %s user", deptId))
		if err != nil {
			return nil, err
		}
	}

	return allUsers, nil
}

// larkCodeRateLimited is the body code of a request refused by Lark's frequency limit
const larkCodeRateLimited = 99991400

const larkMaxRetries = 3

// postJSON sends a POST request with JSON body
func (p *LarkSyncerProvider) postJSON(url string, data interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	return p.doLarkRequest(func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

// getWithAuth sends a GET request with authorization header
func (p *LarkSyncerProvider) getWithAuth(url string, accessToken string) ([]byte, error) {
	return p.doLarkRequest(func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

// doLarkRequest sends the request built by newRequest and returns the response body. While Lark
// answers that the app is rate limited, it waits until the window resets (x-ogw-ratelimit-reset,
// capped at a minute; 1s, 2s, 4s without the header) and sends the request again.
func (p *LarkSyncerProvider) doLarkRequest(newRequest func(ctx context.Context) (*http.Request, error)) ([]byte, error) {
	client := p.Syncer.getHttpClient()
	for retry := 0; ; retry++ {
		data, statusCode, header, err := sendLarkRequest(client, newRequest)
		if err != nil {
			return nil, err
		}

		var resp struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.Unmarshal(data, &resp)
		if statusCode != http.StatusTooManyRequests && resp.Code != larkCodeRateLimited {
			return data, nil
		}
		if retry >= larkMaxRetries {
			return nil, fmt.Errorf("Lark API is still rate limited after %d retries: status=%d, code=%d, msg=%s", larkMaxRetries, statusCode, resp.Code, resp.Msg)
		}

		wait := time.Second << retry
		if reset, err := strconv.Atoi(header.Get("x-ogw-ratelimit-reset")); err == nil && reset > 0 {
			wait = time.Duration(min(reset, 60)) * time.Second
		}
		time.Sleep(wait)
	}
}

func sendLarkRequest(client *http.Client, newRequest func(ctx context.Context) (*http.Request, error)) ([]byte, int, http.Header, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := newRequest(ctx)
	if err != nil {
		return nil, 0, nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, nil, err
	}

	return data, resp.StatusCode, resp.Header, nil
}

// getLarkUsers gets all users from Lark API
func (p *LarkSyncerProvider) getLarkUsers() ([]*OriginalUser, error) {
	// Get access token
	accessToken, err := p.getLarkAccessToken()
	if err != nil {
		return nil, err
	}

	depts := p.takeLarkDepartmentSnapshot()
	if depts == nil {
		depts, err = p.getLarkDepartments(accessToken)
		if err != nil {
			return nil, err
		}
	}

	// Match the OAuth provider's app-scoped open_id, which is also available
	// when the app cannot read user_id. Both providers must use the same app.
	userMap := make(map[string]*LarkUser)
	// The departments whose listing returned the user, since department_ids needs a
	// permission that not every app has
	listedDeptIds := map[string][]string{}
	for _, deptId := range depts.userDepartmentIds() {
		users, err := p.getLarkUsersFromDept(accessToken, deptId)
		if err != nil {
			return nil, err
		}

		for _, user := range users {
			if user == nil || user.OpenId == "" {
				return nil, fmt.Errorf("Lark user in department %s is missing open_id; refusing an incomplete identity snapshot", deptId)
			}
			if _, exists := userMap[user.OpenId]; !exists {
				userMap[user.OpenId] = user
			}
			if deptId != "0" {
				listedDeptIds[user.OpenId] = append(listedDeptIds[user.OpenId], deptId)
			}
		}
	}

	// Convert Lark users to Casdoor OriginalUser
	originalUsers := []*OriginalUser{}
	larkUsers := []*LarkUser{}
	userIds := &larkUserIds{byOpenId: map[string]string{}, byUserId: map[string]string{}}
	for _, larkUser := range userMap {
		originalUser := p.larkUserToOriginalUser(larkUser)
		originalUser.Groups = p.getLarkUserGroupIds(larkUser, listedDeptIds[larkUser.OpenId], depts)
		originalUsers = append(originalUsers, originalUser)
		larkUsers = append(larkUsers, larkUser)
		userId := util.GetId(p.Syncer.Organization, originalUser.Name)
		userIds.byOpenId[larkUser.OpenId] = userId
		if larkUser.UserId != "" {
			userIds.byUserId[larkUser.UserId] = userId
		}
	}

	// Leaders and user custom fields point at open_ids, which become Casdoor user IDs once all users are known
	for i, originalUser := range originalUsers {
		originalUser.Properties = p.getLarkUserProperties(larkUsers[i], userIds)
	}

	return originalUsers, nil
}

// larkPropertyPrefix marks the user properties owned by the Lark syncer
const larkPropertyPrefix = "lark_"

var larkEmployeeTypes = map[int64]string{
	1: "regular",
	2: "intern",
	3: "outsourcing",
	4: "labor",
	5: "consultant",
}

// truncateLarkValue fits a synced value into the varchar(100) user columns
func truncateLarkValue(value string) string {
	runes := []rune(value)
	if len(runes) > 100 {
		runes = runes[:100]
	}
	return string(runes)
}

// larkProfileFields are the Lark fields that TableColumns may map since the profile sync, whose
// values may not fit the column they are mapped to
var larkProfileFields = []string{"nickname", "description", "employee_no", "city", "country", "leader_user_id"}

// larkUserIds maps the open_ids and user_ids of the synced users to their Casdoor user IDs
type larkUserIds struct {
	byOpenId map[string]string
	byUserId map[string]string
}

// getLarkUserProperties returns the "lark_" properties of a user. Users that are not in userIds
// (nil before all users are known) are referred to by their Lark ID.
func (p *LarkSyncerProvider) getLarkUserProperties(larkUser *LarkUser, userIds *larkUserIds) map[string]string {
	properties := map[string]string{}
	set := func(key string, value string) {
		if value != "" {
			properties[larkPropertyPrefix+key] = value
		}
	}
	userRef := func(id string, byUserId bool) string {
		if userIds == nil {
			return id
		}
		if userId, ok := userIds.byOpenId[id]; ok {
			return userId
		}
		// A custom field may refer to a user by user_id
		if userId, ok := userIds.byUserId[id]; ok && byUserId {
			return userId
		}
		return id
	}

	set("employee_no", string(larkUser.EmployeeNo))
	if larkUser.JoinTime > 0 {
		set("join_time", time.Unix(int64(larkUser.JoinTime), 0).UTC().Format(time.RFC3339))
	}
	set("en_name", larkUser.EnName)
	set("nickname", string(larkUser.Nickname))
	if larkUser.EmployeeType != 0 {
		employeeType, ok := larkEmployeeTypes[int64(larkUser.EmployeeType)]
		if !ok {
			// A tenant-defined type, whose name needs another API
			employeeType = strconv.FormatInt(int64(larkUser.EmployeeType), 10)
		}
		set("employee_type", employeeType)
	}
	if larkUser.IsTenantManager != nil {
		set("is_tenant_manager", strconv.FormatBool(bool(*larkUser.IsTenantManager)))
	}
	set("leader", userRef(string(larkUser.LeaderUserId), false))

	for _, attr := range larkUser.CustomAttrs {
		value := attr.Value
		var display string
		switch attr.Type {
		case "HREF":
			display = cmp.Or(value.Url, value.PcUrl, value.Text)
		case "ENUMERATION":
			display = cmp.Or(value.OptionValue, value.OptionId)
		case "PICTURE_ENUM":
			display = cmp.Or(value.Name, value.OptionValue, value.PictureUrl, value.OptionId)
		case "GENERIC_USER":
			if value.GenericUser != nil {
				display = userRef(value.GenericUser.Id, true)
			}
		default:
			display = cmp.Or(value.Text, value.OptionValue, value.Url)
		}
		set("custom_"+attr.Id, display)
	}

	return properties
}

// mergeLarkProfile keeps what an admin set by hand on an existing user: properties that are not
// "lark_" ones, and the location, region and bio when Lark has none
func mergeLarkProfile(oldUser *User, user *User) {
	user.Location = cmp.Or(user.Location, oldUser.Location)
	user.Region = cmp.Or(user.Region, oldUser.Region)
	user.Bio = cmp.Or(user.Bio, oldUser.Bio)

	properties := map[string]string{}
	for key, value := range oldUser.Properties {
		if !strings.HasPrefix(key, larkPropertyPrefix) {
			properties[key] = value
		}
	}
	for key, value := range user.Properties {
		if strings.HasPrefix(key, larkPropertyPrefix) {
			properties[key] = value
		}
	}
	user.Properties = properties
}

// getLarkProfileHash returns the synced profile fields of a user in a stable order, "" if all are empty
func getLarkProfileHash(user *OriginalUser) string {
	values := []string{}
	for key, value := range map[string]string{"location": user.Location, "region": user.Region, "bio": user.Bio} {
		if value != "" {
			values = append(values, key+"="+value)
		}
	}
	for key, value := range user.Properties {
		if strings.HasPrefix(key, larkPropertyPrefix) && value != "" {
			values = append(values, key+"="+value)
		}
	}
	slices.Sort(values)
	return strings.Join(values, "\n")
}

// getLarkUserGroupIds returns the group IDs of the user's departments covered by the syncer, from
// the user's department_ids and from the departments whose listing returned the user
func (p *LarkSyncerProvider) getLarkUserGroupIds(larkUser *LarkUser, listedDeptIds []string, depts *larkDepartments) []string {
	groupIds := []string{}
	for _, deptId := range slices.Concat(larkUser.DepartmentIds, listedDeptIds) {
		groupId := p.getLarkGroupId(deptId)
		if depts.contains(deptId) && !slices.Contains(groupIds, groupId) {
			groupIds = append(groupIds, groupId)
		}
	}
	return groupIds
}

// getLarkUserFieldValue returns a Lark user field by name, for TableColumns mapping.
// The second return value is false for unknown fields so a typo in the mapping
// skips the field instead of overwriting it with an empty value.
func (p *LarkSyncerProvider) getLarkUserFieldValue(larkUser *LarkUser, field string) (string, bool) {
	switch field {
	case "user_id":
		return larkUser.UserId, true
	case "union_id":
		return larkUser.UnionId, true
	case "open_id":
		return larkUser.OpenId, true
	case "name":
		return larkUser.Name, true
	case "en_name":
		return larkUser.EnName, true
	case "email":
		return larkUser.Email, true
	case "enterprise_email":
		return larkUser.EnterpriseEmail, true
	case "mobile":
		return larkUser.Mobile, true
	case "job_title":
		return larkUser.JobTitle, true
	case "nickname":
		return string(larkUser.Nickname), true
	case "description":
		return string(larkUser.Description), true
	case "employee_no":
		return string(larkUser.EmployeeNo), true
	case "city":
		return string(larkUser.City), true
	case "country":
		return string(larkUser.Country), true
	case "leader_user_id":
		return string(larkUser.LeaderUserId), true
	default:
		return "", false
	}
}

// larkUserToOriginalUser converts Lark user to Casdoor OriginalUser
func (p *LarkSyncerProvider) larkUserToOriginalUser(larkUser *LarkUser) *OriginalUser {
	// Use user_id as name, fallback to union_id or open_id
	userName := larkUser.UserId
	if userName == "" && larkUser.UnionId != "" {
		userName = larkUser.UnionId
	}
	if userName == "" && larkUser.OpenId != "" {
		userName = larkUser.OpenId
	}

	// Keep existing user_id-based IDs stable; use the same fallback as the
	// OAuth username when user_id is unavailable instead of an empty ID.
	email := larkUser.Email
	if email == "" {
		email = larkUser.EnterpriseEmail
	}

	user := &OriginalUser{
		Id:          userName,
		Name:        userName,
		DisplayName: larkUser.Name,
		Email:       email,
		Phone:       larkUser.Mobile,
		Title:       larkUser.JobTitle,
		Location:    truncateLarkValue(strings.TrimSpace(string(larkUser.City))),
		Region:      truncateLarkValue(strings.TrimSpace(string(larkUser.Country))),
		Bio:         truncateLarkValue(strings.TrimSpace(string(larkUser.Description))),
		Address:     []string{},
		Properties:  p.getLarkUserProperties(larkUser, nil),
		Groups:      []string{},
		Lark:        larkUser.OpenId, // Must match idp.LarkIdProvider's UserInfo.Id.
	}

	// TableColumns can override individual fields, e.g. map
	// enterprise_email -> Email on tenants that only have an enterprise mailbox.
	// Empty values are skipped so the default columns (user_id -> Id, email -> Email)
	// cannot blank the fallbacks above when the app cannot read those fields.
	for _, tableColumn := range p.Syncer.TableColumns {
		if value, ok := p.getLarkUserFieldValue(larkUser, tableColumn.Name); ok && value != "" {
			if slices.Contains(larkProfileFields, tableColumn.Name) {
				value = truncateLarkValue(value)
			}
			p.Syncer.setUserByKeyValue(user, tableColumn.CasdoorName, value)
		}
	}

	// Set avatar if available
	if larkUser.Avatar != nil {
		if larkUser.Avatar.Avatar240 != "" {
			user.Avatar = larkUser.Avatar.Avatar240
		} else if larkUser.Avatar.Avatar72 != "" {
			user.Avatar = larkUser.Avatar.Avatar72
		}
	}

	// Set gender
	switch larkUser.Gender {
	case 1:
		user.Gender = "Male"
	case 2:
		user.Gender = "Female"
	default:
		user.Gender = ""
	}

	// Set IsForbidden based on status
	// User is forbidden if frozen, resigned, not activated, or exited
	if larkUser.Status != nil {
		if larkUser.Status.IsFrozen || larkUser.Status.IsResigned || !larkUser.Status.IsActivated || larkUser.Status.IsExited {
			user.IsForbidden = true
		} else {
			user.IsForbidden = false
		}
	}

	// Set CreatedTime to current time if not set
	if user.CreatedTime == "" {
		user.CreatedTime = util.GetCurrentTime()
	}

	return user
}

// getLarkGroupId returns the Casdoor group ID ("organization/name") of a Lark department. The
// open_department_id is used as the group name because department names are not unique.
func (p *LarkSyncerProvider) getLarkGroupId(deptId string) string {
	return util.GetId(p.Syncer.Organization, deptId)
}

// isLarkDepartmentGroup tells whether a group ID belongs to a group synced from a Lark department
func isLarkDepartmentGroup(groupId string) bool {
	name := groupId[strings.LastIndex(groupId, "/")+1:]
	return strings.HasPrefix(name, "od-")
}

// mergeLarkDepartmentGroups replaces the Lark department groups of a user with the synced ones,
// keeping the groups assigned by hand
func mergeLarkDepartmentGroups(oldGroups []string, departmentGroups []string) []string {
	groups := []string{}
	for _, groupId := range oldGroups {
		if !isLarkDepartmentGroup(groupId) && !slices.Contains(groups, groupId) {
			groups = append(groups, groupId)
		}
	}
	for _, groupId := range departmentGroups {
		if !slices.Contains(groups, groupId) {
			groups = append(groups, groupId)
		}
	}
	return groups
}

// larkDepartmentToOriginalGroup converts Lark department to Casdoor OriginalGroup
func (p *LarkSyncerProvider) larkDepartmentToOriginalGroup(dept *LarkDepartment, parentId string) *OriginalGroup {
	displayName := dept.Name
	if displayName == "" {
		displayName = dept.OpenDepartmentId
	}

	return &OriginalGroup{
		Id:          p.getLarkGroupId(dept.OpenDepartmentId),
		Name:        dept.OpenDepartmentId,
		DisplayName: displayName,
		Type:        "department",
		ParentId:    parentId,
	}
}

// GetOriginalGroups retrieves all groups (departments) from Lark
func (p *LarkSyncerProvider) GetOriginalGroups() ([]*OriginalGroup, error) {
	p.Syncer.larkDepartments = nil

	accessToken, err := p.getLarkAccessToken()
	if err != nil {
		return nil, err
	}

	depts, err := p.getLarkDepartments(accessToken)
	if err != nil {
		return nil, err
	}

	p.Syncer.larkDepartments = &larkDepartmentSnapshot{
		rootIds:   p.getLarkRootIdsKey(),
		createdAt: time.Now(),
		depts:     depts,
	}

	originalGroups := []*OriginalGroup{}
	for _, dept := range depts.list {
		originalGroups = append(originalGroups, p.larkDepartmentToOriginalGroup(dept, depts.parents[dept.OpenDepartmentId]))
	}

	return originalGroups, nil
}

// GetOriginalUserGroups retrieves the group IDs that a user belongs to (not implemented yet)
func (p *LarkSyncerProvider) GetOriginalUserGroups(userId string) ([]string, error) {
	// TODO: Implement Lark user group membership sync
	return []string{}, nil
}
