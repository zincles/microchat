package flow

import "testing"

const validGraph = `{
  "version": 1,
  "nodes": [
    {"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
    {"id":"llm","type":"llm","position":{"x":200,"y":0},"data":{
      "provider":"dummy","model":"dummy","max_retry":3,"width":260,"height":400,
      "prompts":[{"id":"p1","role":"system","content":"写"}]
    }},
    {"id":"out","type":"output","position":{"x":400,"y":0},"data":{}}
  ],
  "edges": [
    {"id":"e1","source":"in","sourceHandle":"out","target":"llm","targetHandle":"prompt-p1"},
    {"id":"e2","source":"llm","sourceHandle":"text","target":"out","targetHandle":"text"}
  ]
}`

func TestValidGraph(t *testing.T) {
	if err := ValidateRaw([]byte(validGraph)); err != nil {
		t.Fatal(err)
	}
}

func TestRejectUnknownGraphField(t *testing.T) {
	if err := ValidateRaw([]byte(`{"version":1,"nodes":[],"edges":[],"extra":true}`)); err == nil {
		t.Fatal("未知图字段应该被拒绝")
	}
}

func TestRejectUnknownNodeDataField(t *testing.T) {
	raw := `{"version":1,"nodes":[
		{"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
		{"id":"llm","type":"llm","position":{"x":1,"y":0},"data":{"prompts":[{"id":"p","role":"system","content":"x"}],"bogus":1}},
		{"id":"out","type":"output","position":{"x":2,"y":0},"data":{}}
	],"edges":[
		{"id":"e1","source":"in","sourceHandle":"out","target":"llm","targetHandle":"prompt-p"},
		{"id":"e2","source":"llm","sourceHandle":"text","target":"out","targetHandle":"text"}
	]}`
	if err := ValidateRaw([]byte(raw)); err == nil {
		t.Fatal("未知节点 data 字段应该被拒绝")
	}
}

func TestRejectMissingPort(t *testing.T) {
	raw := `{"version":1,"nodes":[
		{"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
		{"id":"out","type":"output","position":{"x":1,"y":0},"data":{}}
	],"edges":[{"id":"e","source":"in","target":"out"}]}`
	if err := ValidateRaw([]byte(raw)); err == nil {
		t.Fatal("空端口应该被拒绝")
	}
}

func TestRejectCycle(t *testing.T) {
	raw := `{"version":1,"nodes":[
		{"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
		{"id":"a","type":"text","position":{"x":1,"y":0},"data":{"text":"a"}},
		{"id":"b","type":"text","position":{"x":2,"y":0},"data":{"text":"b"}},
		{"id":"out","type":"output","position":{"x":3,"y":0},"data":{}}
	],"edges":[
		{"id":"e1","source":"in","sourceHandle":"out","target":"a","targetHandle":"in"},
		{"id":"e2","source":"a","sourceHandle":"out","target":"b","targetHandle":"in"},
		{"id":"e3","source":"b","sourceHandle":"out","target":"a","targetHandle":"in"}
	]}`
	if err := ValidateRaw([]byte(raw)); err == nil {
		t.Fatal("有环的图应该被拒绝")
	}
}
