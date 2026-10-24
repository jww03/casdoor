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

// lark-permission-check reports which directory fields a Lark/Feishu app can read for the Casdoor
// Lark syncer, and the permission scopes of the missing ones. It reads its settings from the
// environment so that the app secret stays out of the shell history:
//
//	LARK_APP_ID               app_id of the app (required)
//	LARK_APP_SECRET           app_secret of the app (required)
//	LARK_HOST                 API endpoint, default https://open.feishu.cn
//	LARK_ROOT_DEPARTMENT_IDS  root departments of the syncer, comma separated, default the whole company
//
// Usage:
//
//	read -s LARK_APP_SECRET && export LARK_APP_SECRET
//	LARK_APP_ID=cli_xxx go run ./tools/lark-permission-check
//
// It exits with 1 when the token cannot be obtained, a root department cannot be resolved or no
// users are visible, and with 0 otherwise, even when optional fields are missing.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/casdoor/casdoor/object"
)

func main() {
	appId := strings.TrimSpace(os.Getenv("LARK_APP_ID"))
	appSecret := strings.TrimSpace(os.Getenv("LARK_APP_SECRET"))
	if appId == "" || appSecret == "" {
		fmt.Fprintln(os.Stderr, "LARK_APP_ID and LARK_APP_SECRET must be set")
		os.Exit(2)
	}
	host := strings.TrimSpace(os.Getenv("LARK_HOST"))
	if host == "" {
		host = "https://open.feishu.cn"
	}

	syncer := &object.Syncer{
		Type: "Lark",
		// "built-in" lets the tool reach any host, e.g. a proxy, as it runs on the admin's machine
		Organization:      "built-in",
		Host:              host,
		User:              appId,
		Password:          appSecret,
		RootDepartmentIds: os.Getenv("LARK_ROOT_DEPARTMENT_IDS"),
	}

	report, err := object.CheckLarkSyncerPermissions(syncer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
		os.Exit(1)
	}

	fmt.Print(report.String())
	if len(report.Errors) > 0 {
		os.Exit(1)
	}
}
