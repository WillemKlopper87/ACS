package main

import (
	"acs/internal/jobs"
	"acs/internal/parameters"
	"acs/internal/store"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestUSPVerifiedWriteAndInstanceEvidence(t *testing.T) {
	repo, device := newDispatcherTestDB(t)
	ctx := context.Background()
	db, err := store.Open(ctx, os.Getenv("ACS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	params := parameters.NewRepository(db)
	if err := params.Upsert(ctx, device, map[string]parameters.CachedValue{"Device.DeviceInfo.SoftwareVersion": {Value: "1.0", UpdatedAt: time.Now(), Source: parameters.SourceInform}}); err != nil {
		t.Fatal(err)
	}
	identity := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	conn := &captureConn{id: agent}
	registerAgent(identity, registry, device, conn)
	dispatch := newDispatcher(repo, identity, registry, ctrl, slog.Default())
	dispatch.paramsRepo = params
	job, err := repo.Create(ctx, device, jobs.TypeSetParameter, jobs.SetParameterPayload{Verify: true, Parameters: []jobs.ParameterWrite{{Name: "Device.WiFi.SSID.2.SSID", Value: "lab"}}}, "lab")
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatch.tryDispatch(ctx, device); err != nil {
		t.Fatal(err)
	}
	decode := func(index int) *uspproto.Msg {
		t.Helper()
		record, err := usp.DecodeRecord(conn.sent[index], agent)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := usp.DecodeMsg(record.Payload)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	request := decode(0)
	ack := &uspproto.Msg{Header: &uspproto.Header{MsgId: request.GetHeader().GetMsgId(), MsgType: uspproto.Header_SET_RESP}, Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{RespType: &uspproto.Response_SetResp{SetResp: &uspproto.SetResp{}}}}}}
	if dispatch.handleResponse("different-agent", ack) {
		t.Fatal("response from wrong endpoint accepted")
	}
	if !dispatch.handleResponse(agent, ack) {
		t.Fatal("ack did not match")
	}
	if len(conn.sent) != 2 || decode(1).GetBody().GetRequest().GetGet() == nil {
		t.Fatal("readback not dispatched")
	}
	if current, _ := repo.ByID(ctx, job.ID); current.Status != jobs.StatusRPCSent {
		t.Fatal("write succeeded before verification", current.Status)
	}
	readback := getResp(decode(1).GetHeader().GetMsgId(), map[string]string{"SSID": "lab"})
	readbackResult := readback.GetBody().GetResponse().GetGetResp().ReqPathResults[0]
	readbackResult.RequestedPath = "Device.WiFi.SSID.2.SSID"
	readbackResult.ResolvedPathResults[0].ResolvedPath = "Device.WiFi.SSID.2."
	if !dispatch.handleResponse(agent, readback) {
		t.Fatal("readback did not match")
	}
	if dispatch.handleResponse(agent, readback) {
		t.Fatal("duplicate response matched completed operation")
	}
	summary, err := params.OperationSummary(ctx, device, "1.0")
	if err != nil || !summary["write_tested"] {
		t.Fatal(summary, err)
	}
	current, _ := repo.ByID(ctx, job.ID)
	if current.Status != jobs.StatusSuccess {
		t.Fatal(current.Status)
	}
	instanceJob, err := repo.Create(ctx, device, jobs.TypeGetParameter, jobs.GetParameterPayload{Instances: true, Firmware: "1.0", Paths: []string{"Device.WiFi.SSID."}}, "lab")
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatch.tryDispatch(ctx, device); err != nil {
		t.Fatal(err)
	}
	request = decode(2)
	response := &uspproto.Msg{Header: &uspproto.Header{MsgId: request.GetHeader().GetMsgId(), MsgType: uspproto.Header_GET_INSTANCES_RESP}, Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{RespType: &uspproto.Response_GetInstancesResp{GetInstancesResp: &uspproto.GetInstancesResp{ReqPathResults: []*uspproto.GetInstancesResp_RequestedPathResult{{RequestedPath: "Device.WiFi.SSID.", CurrInsts: []*uspproto.GetInstancesResp_CurrInstance{{InstantiatedObjPath: "Device.WiFi.SSID.2."}}}}}}}}}}
	if !dispatch.handleResponse(agent, response) {
		t.Fatal("instances did not match")
	}
	evidence, err := params.Instances(ctx, device)
	if err != nil || len(evidence) != 1 || len(evidence[0].Instances) != 1 {
		t.Fatal(evidence, err)
	}
	current, _ = repo.ByID(ctx, instanceJob.ID)
	if current.Status != jobs.StatusSuccess {
		t.Fatal(current.Status, current.FaultString)
	}
}
