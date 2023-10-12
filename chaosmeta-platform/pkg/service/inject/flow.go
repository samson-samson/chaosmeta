/*
 * Copyright 2022-2023 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package inject

import (
	models "chaosmeta-platform/pkg/models/common"
	"chaosmeta-platform/pkg/models/inject/basic"
	"chaosmeta-platform/util/log"
	"context"
	"fmt"
)

func InitFlow() error {
	flow := basic.FlowInject{}
	if _, err := models.GetORM().Raw(fmt.Sprintf("TRUNCATE TABLE %s", flow.TableName())).Exec(); err != nil {
		return err
	}
	ctx := context.Background()
	return InitHttpFlow(ctx)
}

func initFlowCommon(ctx context.Context, injectId int) error {
	argDuration := basic.Args{InjectId: injectId, ExecType: ExecFlowCommon, Key: "duration", KeyCn: "duration", ValueType: "string", Unit: "s,m,h", UnitCn: "s,m,h", DescriptionCn: "持续度量时间", Description: "Duration measurement time"}
	argsParallelism := basic.Args{InjectId: injectId, ExecType: ExecFlowCommon, Key: "parallelism", KeyCn: "parallelism", ValueType: "int", ValueRule: ">0", DescriptionCn: "并发度", Description: "Concurrency"}
	argsSource := basic.Args{InjectId: injectId, ExecType: ExecFlowCommon, Key: "source", KeyCn: "source", ValueType: "int", ValueRule: ">0", DescriptionCn: "请求源", Description: "Request source"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argDuration, &argsParallelism, &argsSource})
}

func InitHttpFlow(ctx context.Context) error {
	var (
		httpFlow = basic.FlowInject{FlowType: "HTTP", Name: "HTTP", NameCn: "HTTP", Description: "continuously inject http request traffic to the target http server", DescriptionCn: "对目标http服务器持续注入http请求流量"}
	)
	if err := basic.InsertFlowInject(&httpFlow); err != nil {
		return err
	}
	return InitHttpFlowArgs(ctx, httpFlow)
}

func InitHttpFlowArgs(ctx context.Context, flowInject basic.FlowInject) error {
	if err := initFlowCommon(ctx, flowInject.Id); err != nil {
		log.Error(err)
		return err
	}
	argsHost := basic.Args{InjectId: flowInject.Id, ExecType: ExecFlow, Key: "host", KeyCn: "目标机器", ValueType: "string", DescriptionCn: "目标端口,可选值：ip、域名", Description: "Destination port:optional values: ip, domain name"}
	argsPort := basic.Args{InjectId: flowInject.Id, ExecType: ExecFlow, Key: "port", KeyCn: "目标端口", ValueType: "string", DescriptionCn: "目标端口, 单个端口号", Description: "Destination port, a single port number"}
	argsPath := basic.Args{InjectId: flowInject.Id, ExecType: ExecFlow, Key: "path", KeyCn: "请求path", ValueType: "string", DescriptionCn: "url路径", Description: "URL path"}
	argsHeader := basic.Args{InjectId: flowInject.Id, ExecType: ExecFlow, Key: "header", KeyCn: "请求header", ValueType: "string", DescriptionCn: "键值对列表，格式：'k1:v1,k2:v2'", Description: "List of key-value pairs, format: 'k1:v1,k2:v2'"}
	argsMethod := basic.Args{InjectId: flowInject.Id, ExecType: ExecFlow, Key: "method", KeyCn: "方法", ValueType: "string", ValueRule: "GET,POST", DescriptionCn: "请求方法", Description: "Request method"}
	argsBody := basic.Args{InjectId: flowInject.Id, ExecType: ExecFlow, Key: "body", KeyCn: "请求数据", ValueType: "string", Description: "Request data", DescriptionCn: "请求数据"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argsHost, &argsPort, &argsPath, &argsHeader, &argsMethod, &argsBody})
}

func (i *InjectService) ListFlows(ctx context.Context, orderBy string, page, pageSize int) (int64, []basic.FlowInject, error) {
	total, targets, err := basic.ListFlowInjects(orderBy, page, pageSize)
	return total, targets, err
}
