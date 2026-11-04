// English: This integration test uses a real official SDK MCP server, PostgreSQL, Approval, and Tool Gateway.
// 中文：本集成测试使用真实官方 SDK MCP Server、PostgreSQL、审批和 Tool Gateway。
package integration

import (
	"context"
	"encoding/json"
	"personal-ai-os/internal/supervisor"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-ai-os/internal/app/store"
	"personal-ai-os/internal/approval"
	"personal-ai-os/internal/execution"
	"personal-ai-os/internal/goal"
	"personal-ai-os/internal/policy"
	toolgateway "personal-ai-os/internal/tool/gateway"
	toolmcp "personal-ai-os/internal/tool/mcp"
	"personal-ai-os/internal/tool/registry"
	"personal-ai-os/internal/task"
	"personal-ai-os/migrations"
)

func TestPostgresRealMCPGatewayApprovalAuditAndReplay(t *testing.T) {
	pool, repository := integrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, pool); err != nil { t.Fatalf("apply migrations: %v", err) }
	goalItem, err := repository.Create(ctx, goal.Goal{OwnerID:testOwnerID,Title:uniqueLabel("mcp-gateway-goal"),Priority:3,Status:goal.Active})
	if err != nil { t.Fatalf("create Goal: %v",err) }
	taskItem, err := repository.Tasks().Create(ctx,task.Task{GoalID:goalItem.ID,Title:uniqueLabel("mcp-gateway-task"),Priority:3,Status:task.Ready})
	if err != nil { t.Fatalf("create Task: %v",err) }
	serverCalls:=new(atomic.Int32)
	mcpServer:=sdk.NewServer(&sdk.Implementation{Name:"integration-mcp",Version:"1.0.0"},nil)
	mcpServer.AddTool(&sdk.Tool{Name:"lookup",Description:"Integration lookup",InputSchema:json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)},func(_ context.Context,request *sdk.CallToolRequest)(*sdk.CallToolResult,error){serverCalls.Add(1);var input struct{Query string `json:"query"`};if err:=json.Unmarshal(request.Params.Arguments,&input);err!=nil{return nil,err};return &sdk.CallToolResult{Content:[]sdk.Content{&sdk.TextContent{Text:"MCP result: "+input.Query}}},nil})
	httpHandler:=sdk.NewStreamableHTTPHandler(func(*http.Request)*sdk.Server{return mcpServer},&sdk.StreamableHTTPOptions{Stateless:true,JSONResponse:true})
	httpServer:=httptest.NewServer(httpHandler);defer httpServer.Close()
	parsed,_:=url.Parse(httpServer.URL)
	tools:=registry.New()
	manager,err:=toolmcp.NewManager(tools,toolmcp.Options{HealthInterval:time.Second});if err!=nil{t.Fatal(err)}
	if err:=manager.Start(ctx,[]toolmcp.ServerConfig{{ID:"integration",Name:"Integration MCP",Transport:toolmcp.TransportStreamableHTTP,Endpoint:httpServer.URL,AllowedHosts:[]string{parsed.Hostname()},AllowedTools:[]string{"lookup"},Enabled:true}});err!=nil{t.Fatalf("start MCP manager: %v",err)}
	defer manager.Close()
	deadline:=time.Now().Add(5*time.Second)
	for time.Now().Before(deadline){statuses:=manager.Status();if len(statuses)==1&&statuses[0].ConnectionStatus=="connected"&&statuses[0].ToolCount==1{break};time.Sleep(10*time.Millisecond)}
	statuses:=manager.Status();if len(statuses)!=1||statuses[0].ConnectionStatus!="connected"||statuses[0].ToolCount!=1{t.Fatalf("MCP discovery status=%+v",statuses)}
	var identity registry.Identity
	for _,entry:=range tools.List(){if entry.Definition.Source==registry.SourceMCP{identity=entry.Definition.Identity}}
	if identity.ToolID==""{t.Fatal("real MCP tool was not registered")}

	durable:=store.NewDurableExecutionStore(pool)
	enqueued,_,err:=durable.Enqueue(ctx,newExecutionEnqueueRequest(taskItem.ID,"mcp-agent",uniqueLabel("mcp-run"),[]byte(`{"kind":"mcp-gateway"}`),2,true))
	if err!=nil{t.Fatalf("enqueue durable execution: %v",err)}
	claimed,err:=durable.Claim(ctx,"mcp-worker",30*time.Second);if err!=nil||claimed.ID!=enqueued.ID{t.Fatalf("claim execution=%+v err=%v",claimed,err)}
	lease:=execution.Lease{ExecutionID:claimed.ID,WorkerID:claimed.WorkerID,Fencing:claimed.FencingToken,LeaseUntil:claimed.LeaseUntil}
	if err:=durable.Start(ctx,lease);err!=nil{t.Fatalf("start execution: %v",err)}
	request:=toolgateway.Request{GoalID:goalItem.ID,TaskID:taskItem.ID,ExecutionID:claimed.ID,StepID:"mcp-step",AgentID:"mcp-agent",Identity:identity,IdempotencyKey:"mcp-invocation-key",Lease:lease,Arguments:json.RawMessage(`{"query":"real server"}`)}
	grants:=toolgateway.StaticGrants{identity.ProviderID+"/"+identity.ToolID+"@"+identity.Version:{Allowed:true,Autonomy:policy.A0}}
	postgres:=store.NewPostgres(pool)
	gate:=&toolgateway.Gateway{Registry:tools,Authorizer:toolgateway.PolicyAuthorizer{Grants:grants},Leases:postgres,Store:postgres}
	pending,err:=gate.Execute(ctx,request)
	if err!=nil||pending.Status!="waiting_approval"||pending.ApprovalID==""||serverCalls.Load()!=0{t.Fatalf("MCP pre-approval result=%+v err=%v calls=%d",pending,err,serverCalls.Load())}
	if err:=durable.Fail(ctx,lease,execution.FailureApprovalNeeded,"MCP Tool requires approval",0);err!=nil{t.Fatalf("persist waiting Approval: %v",err)}
	approved,err:=repository.Approvals().Decide(ctx,testOwnerID,pending.ApprovalID,approval.Approved,time.Now().UTC());if err!=nil||approved.Status!=approval.Approved{t.Fatalf("approve MCP Tool=%+v err=%v",approved,err)}
	if err:=durable.ResumeApproval(ctx,testOwnerID,pending.ApprovalID);err!=nil{t.Fatalf("resume approved MCP invocation: %v",err)}
	resumed,err:=durable.Claim(ctx,"mcp-worker-resumed",30*time.Second);if err!=nil||resumed.ID!=claimed.ID||resumed.FencingToken<=claimed.FencingToken{t.Fatalf("claim approved MCP execution=%+v err=%v",resumed,err)}
	lease=execution.Lease{ExecutionID:resumed.ID,WorkerID:resumed.WorkerID,Fencing:resumed.FencingToken,LeaseUntil:resumed.LeaseUntil};if err:=durable.Start(ctx,lease);err!=nil{t.Fatalf("start approved execution: %v",err)}
	request.Lease=lease;request.ApprovalID=pending.ApprovalID
	result,err:=gate.Execute(ctx,request)
	if err!=nil||result.Status!="succeeded"||serverCalls.Load()!=1||!strings.Contains(string(result.Result),"MCP result: real server"){t.Fatalf("MCP Gateway result=%+v err=%v calls=%d",result,err,serverCalls.Load())}
	replay,err:=gate.Execute(ctx,request)
	if err!=nil||!replay.Replayed||replay.Status!="succeeded"||serverCalls.Load()!=1{t.Fatalf("MCP replay=%+v err=%v calls=%d",replay,err,serverCalls.Load())}
	var auditCount,outboxCount int
	if err:=pool.QueryRow(ctx,"SELECT count(*) FROM audit_records WHERE resource='tool_invocations' AND resource_id=$1",result.InvocationID).Scan(&auditCount);err!=nil||auditCount<2{t.Fatalf("MCP invocation audit count=%d err=%v",auditCount,err)}
	if err:=pool.QueryRow(ctx,"SELECT count(*) FROM event_outbox WHERE aggregate_type='tool_invocations' AND aggregate_id=$1",result.InvocationID).Scan(&outboxCount);err!=nil||outboxCount<2{t.Fatalf("MCP invocation outbox count=%d err=%v",outboxCount,err)}
	completedAt:=time.Now().UTC()
	verifiedRun,_:=json.Marshal(supervisor.Execution{ID:resumed.ID,TaskID:taskItem.ID,AgentID:"mcp-agent",Summary:"independently reviewed result",Outcome:"succeeded",InputTokens:3,OutputTokens:4,TotalTokens:7,ModelCalls:2,ToolCalls:1,Evidence:[]supervisor.Evidence{{TaskID:taskItem.ID,Source:"integration-verifier",Description:"test harness independently verified the fixture result",ObservedAt:completedAt}},CompletedAt:completedAt,Verified:true})
	if err:=durable.Complete(ctx,lease,verifiedRun,[]execution.Evidence{{Source:"integration-verifier",Description:"test harness independently verified the fixture result",ObservedAt:completedAt}},true);err!=nil{t.Fatalf("complete verified durable Agent result: %v",err)}
	usage,err:=store.NewModelUsageStore(pool).ModelUsage(ctx,goalItem.ID)
	if err!=nil||usage.ModelCalls!=2||usage.ModelTokens!=7||usage.ToolCalls!=1{t.Fatalf("persisted Goal usage=%+v err=%v",usage,err)}
	cleanupIntegrationRows(t,ctx,pool,[]string{goalItem.ID},[]string{taskItem.ID},nil,nil,nil,[]string{pending.ApprovalID})
}

