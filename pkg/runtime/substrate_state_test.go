// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
)

const testStateNamespace = "scion-broker-state"

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// newStateFakeClientset returns a client-go fake clientset whose Secrets
// behave like the API server's for this store's purposes: every create and
// update assigns a new resourceVersion, an update carrying a stale
// resourceVersion fails with Conflict, and a delete whose resourceVersion
// precondition is stale fails with Conflict. The fake's own object tracker
// stores objects as-is and never checks resourceVersion, so without this
// the store's compare-and-swap could not be exercised at all.
func newStateFakeClientset(objects ...k8sruntime.Object) *k8sfake.Clientset {
	cs := k8sfake.NewSimpleClientset(objects...)
	tracker := cs.Tracker()
	var mu sync.Mutex
	rv := 1000

	cs.PrependReactor("create", "secrets", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		obj := action.(k8stesting.CreateAction).GetObject().(*corev1.Secret).DeepCopy()
		rv++
		obj.ResourceVersion = strconv.Itoa(rv)
		if err := tracker.Create(secretsGVR, obj, action.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, obj.DeepCopy(), nil
	})
	cs.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		obj := action.(k8stesting.UpdateAction).GetObject().(*corev1.Secret).DeepCopy()
		cur, err := tracker.Get(secretsGVR, action.GetNamespace(), obj.Name)
		if err != nil {
			return true, nil, err
		}
		if obj.ResourceVersion != "" && obj.ResourceVersion != cur.(*corev1.Secret).ResourceVersion {
			return true, nil, apierrors.NewConflict(corev1.Resource("secrets"), obj.Name, errors.New("the object has been modified"))
		}
		rv++
		obj.ResourceVersion = strconv.Itoa(rv)
		if err := tracker.Update(secretsGVR, obj, action.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, obj.DeepCopy(), nil
	})
	cs.PrependReactor("delete", "secrets", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		da := action.(k8stesting.DeleteActionImpl)
		cur, err := tracker.Get(secretsGVR, action.GetNamespace(), da.Name)
		if err != nil {
			return true, nil, err
		}
		if pre := da.DeleteOptions.Preconditions; pre != nil && pre.ResourceVersion != nil &&
			*pre.ResourceVersion != cur.(*corev1.Secret).ResourceVersion {
			return true, nil, apierrors.NewConflict(corev1.Resource("secrets"), da.Name, errors.New("precondition failed"))
		}
		return true, nil, tracker.Delete(secretsGVR, action.GetNamespace(), da.Name)
	})
	return cs
}

func testState(atespace, actor string) *substrateAgentState {
	return &substrateAgentState{
		ID:    atespace + "/" + actor,
		Phase: substrateStatePending,
		Record: substrateAgentRecord{
			Labels:      map[string]string{"scion.name": actor, "scion.agent": "true"},
			Template:    "claude",
			Project:     "proj",
			ProjectID:   "proj-id",
			ProjectPath: "/p",
			Image:       "img@sha256:abc",
		},
		ControlToken: "tok-" + actor,
		ExecSecrets:  map[string]string{"env:API_KEY": "secret-" + actor},
	}
}

const (
	testAtespaceA = "scion-0123456789abcdef0123456789abcdef"
	testAtespaceB = "scion-fedcba9876543210fedcba9876543210"
)

func TestSubstrateStateObjectName_Schema(t *testing.T) {
	id := testAtespaceA + "/proj--agent"
	sum := sha256.Sum256([]byte(id))
	want := "scion-sb-" + hex.EncodeToString(sum[:])[:40]
	if got := substrateStateObjectName(id); got != want {
		t.Fatalf("substrateStateObjectName = %q, want %q", got, want)
	}
	if strings.Contains(substrateStateObjectName(id), "agent") {
		t.Fatalf("object name must not carry identity text: %q", substrateStateObjectName(id))
	}
}

// TestK8sSecretStateStore_CreateWritesSchema pins the stored object's shape
// (name, type, labels, annotations, data keys and record.json field names):
// every later broker version reads it.
func TestK8sSecretStateStore_CreateWritesSchema(t *testing.T) {
	cs := newStateFakeClientset()
	store := newK8sSecretStateStore(cs, testStateNamespace)
	st := testState(testAtespaceA, "proj--agent")
	if err := store.Create(context.Background(), st); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if st.version == "" {
		t.Fatal("Create did not set version")
	}
	sec, err := cs.CoreV1().Secrets(testStateNamespace).Get(context.Background(), substrateStateObjectName(st.ID), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created secret: %v", err)
	}
	if sec.Type != corev1.SecretTypeOpaque {
		t.Errorf("type = %q, want Opaque", sec.Type)
	}
	wantLabels := map[string]string{
		"app.kubernetes.io/managed-by":  "scion-substrate-broker",
		"scion.substrate/state-version": "1",
		"scion.substrate/atespace":      testAtespaceA,
	}
	for k, v := range wantLabels {
		if sec.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, sec.Labels[k], v)
		}
	}
	if len(sec.Labels) != len(wantLabels) {
		t.Errorf("labels = %v, want exactly %v", sec.Labels, wantLabels)
	}
	if sec.Annotations["scion.substrate/phase"] != "pending" {
		t.Errorf("phase annotation = %q", sec.Annotations["scion.substrate/phase"])
	}
	if v, ok := sec.Annotations["scion.substrate/actor-uid"]; !ok || v != "" {
		t.Errorf("actor-uid annotation = %q (present %v), want present and empty while pending", v, ok)
	}
	var keys []string
	for k := range sec.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "control_token,exec_secrets,id,record.json" {
		t.Errorf("data keys = %v", keys)
	}
	if string(sec.Data["id"]) != st.ID || string(sec.Data["control_token"]) != st.ControlToken {
		t.Errorf("id/control_token not stored verbatim")
	}
	var secrets map[string]string
	if err := json.Unmarshal(sec.Data["exec_secrets"], &secrets); err != nil || secrets["env:API_KEY"] != "secret-proj--agent" {
		t.Errorf("exec_secrets = %s (%v)", sec.Data["exec_secrets"], err)
	}
	var record map[string]any
	if err := json.Unmarshal(sec.Data["record.json"], &record); err != nil {
		t.Fatalf("record.json: %v", err)
	}
	for _, k := range []string{"labels", "template", "harness_config", "project", "project_id", "project_path", "image"} {
		if _, ok := record[k]; !ok {
			t.Errorf("record.json missing %q: %s", k, sec.Data["record.json"])
		}
	}
}

func TestK8sSecretStateStore_CreateDuplicateIsErrStateExists(t *testing.T) {
	store := newK8sSecretStateStore(newStateFakeClientset(), testStateNamespace)
	if err := store.Create(context.Background(), testState(testAtespaceA, "a")); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), testState(testAtespaceA, "a")); !errors.Is(err, errStateExists) {
		t.Fatalf("second Create = %v, want errStateExists", err)
	}
}

func TestK8sSecretStateStore_GetRoundTripAndNotFound(t *testing.T) {
	store := newK8sSecretStateStore(newStateFakeClientset(), testStateNamespace)
	ctx := context.Background()
	if _, err := store.Get(ctx, testAtespaceA+"/missing"); !errors.Is(err, errStateNotFound) {
		t.Fatalf("Get missing = %v, want errStateNotFound", err)
	}
	want := testState(testAtespaceA, "a")
	if err := store.Create(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Phase != want.Phase || got.ControlToken != want.ControlToken ||
		got.ExecSecrets["env:API_KEY"] != want.ExecSecrets["env:API_KEY"] ||
		got.Record.Project != "proj" || got.Record.ProjectPath != "/p" || got.Record.Labels["scion.name"] != "a" ||
		got.version != want.version {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}
}

func TestK8sSecretStateStore_UpdateCAS(t *testing.T) {
	store := newK8sSecretStateStore(newStateFakeClientset(), testStateNamespace)
	ctx := context.Background()
	st := testState(testAtespaceA, "a")
	if err := store.Create(ctx, st); err != nil {
		t.Fatal(err)
	}
	stale := *st

	st.ActorUID = "uid-1"
	st.Phase = substrateStateCommitted
	before := st.version
	if err := store.Update(ctx, st); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if st.version == before {
		t.Fatal("Update did not advance version")
	}
	got, err := store.Get(ctx, st.ID)
	if err != nil || got.Phase != substrateStateCommitted || got.ActorUID != "uid-1" {
		t.Fatalf("after Update Get = %+v, %v", got, err)
	}

	stale.Phase = substrateStateDeleting
	if err := store.Update(ctx, &stale); !errors.Is(err, errStateConflict) {
		t.Fatalf("stale Update = %v, want errStateConflict", err)
	}

	if err := store.Delete(ctx, st.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, st); !errors.Is(err, errStateNotFound) {
		t.Fatalf("Update of deleted object = %v, want errStateNotFound", err)
	}
}

func TestK8sSecretStateStore_Delete(t *testing.T) {
	store := newK8sSecretStateStore(newStateFakeClientset(), testStateNamespace)
	ctx := context.Background()
	if err := store.Delete(ctx, testAtespaceA+"/never", ""); err != nil {
		t.Fatalf("Delete of absent object = %v, want nil (NotFound is success)", err)
	}
	st := testState(testAtespaceA, "a")
	if err := store.Create(ctx, st); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, st.ID, "stale-version"); !errors.Is(err, errStateConflict) {
		t.Fatalf("Delete with stale version = %v, want errStateConflict", err)
	}
	if err := store.Delete(ctx, st.ID, st.version); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, st.ID); !errors.Is(err, errStateNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
	if err := store.Delete(ctx, st.ID, st.version); err != nil {
		t.Fatalf("second Delete = %v, want nil", err)
	}
}

func TestK8sSecretStateStore_ListFiltersByAtespaceAndAll(t *testing.T) {
	cs := newStateFakeClientset()
	store := newK8sSecretStateStore(cs, testStateNamespace)
	ctx := context.Background()
	for _, st := range []*substrateAgentState{
		testState(testAtespaceA, "a1"), testState(testAtespaceA, "a2"), testState(testAtespaceB, "b1"),
	} {
		if err := store.Create(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	// An unrelated Secret in the same namespace is not a state object.
	if _, err := cs.CoreV1().Secrets(testStateNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	ids := func(sts []*substrateAgentState) string {
		var out []string
		for _, s := range sts {
			out = append(out, s.ID)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	gotA, err := store.List(ctx, testAtespaceA)
	if err != nil {
		t.Fatal(err)
	}
	if want := testAtespaceA + "/a1," + testAtespaceA + "/a2"; ids(gotA) != want {
		t.Errorf("List(A) = %s, want %s", ids(gotA), want)
	}
	all, err := store.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("List(\"\") = %s, want all 3", ids(all))
	}
	for _, s := range all {
		if s.version == "" {
			t.Errorf("List entry %s has no version", s.ID)
		}
	}
}

// putRawSecret writes sec directly, bypassing the store, for tamper and
// version tests.
func putRawSecret(t *testing.T, cs *k8sfake.Clientset, sec *corev1.Secret) {
	t.Helper()
	sec.Namespace = testStateNamespace
	if _, err := cs.CoreV1().Secrets(testStateNamespace).Create(context.Background(), sec, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestK8sSecretStateStore_TamperedOrCollidingIDIsNotFound(t *testing.T) {
	logs := captureRuntimeLog(t)
	cs := newStateFakeClientset()
	store := newK8sSecretStateStore(cs, testStateNamespace)
	ctx := context.Background()

	requested := testAtespaceA + "/victim"
	other := testState(testAtespaceA, "attacker")
	sec, err := store.encode(other)
	if err != nil {
		t.Fatal(err)
	}
	// The object sits under the requested id's name but names another id.
	sec.Name = substrateStateObjectName(requested)
	putRawSecret(t, cs, sec)

	if _, err := store.Get(ctx, requested); !errors.Is(err, errStateNotFound) {
		t.Fatalf("Get of tampered object = %v, want errStateNotFound", err)
	}
	all, err := store.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("List returned a tampered object: %+v", all)
	}
	out := logs.String()
	if !strings.Contains(out, sec.Name) {
		t.Errorf("log should name the object %s: %s", sec.Name, out)
	}
	for _, leak := range []string{other.ControlToken, other.ExecSecrets["env:API_KEY"], "attacker", "victim"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks object content %q: %s", leak, out)
		}
	}
}

func TestK8sSecretStateStore_UnknownVersionSkipped(t *testing.T) {
	logs := captureRuntimeLog(t)
	cs := newStateFakeClientset()
	store := newK8sSecretStateStore(cs, testStateNamespace)
	ctx := context.Background()

	st := testState(testAtespaceA, "future")
	sec, err := store.encode(st)
	if err != nil {
		t.Fatal(err)
	}
	sec.Labels[substrateStateVersionKey] = "2"
	putRawSecret(t, cs, sec)

	if _, err := store.Get(ctx, st.ID); !errors.Is(err, errStateNotFound) {
		t.Fatalf("Get of unknown-version object = %v, want errStateNotFound", err)
	}
	all, err := store.List(ctx, testAtespaceA)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("List returned an unknown-version object: %+v", all)
	}
	out := logs.String()
	if !strings.Contains(out, sec.Name) {
		t.Errorf("log should name the skipped object %s: %s", sec.Name, out)
	}
	if strings.Contains(out, st.ControlToken) || strings.Contains(out, st.ExecSecrets["env:API_KEY"]) {
		t.Errorf("log leaks object content: %s", out)
	}
}

func TestK8sSecretStateStore_ListPages(t *testing.T) {
	cs := newStateFakeClientset()
	store := newK8sSecretStateStore(cs, testStateNamespace)
	ctx := context.Background()
	for _, n := range []string{"a1", "a2", "a3"} {
		if err := store.Create(ctx, testState(testAtespaceA, n)); err != nil {
			t.Fatal(err)
		}
	}
	// Serve the list one object per page, as a real API server may.
	var calls int
	cs.PrependReactor("list", "secrets", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		calls++
		objs, err := cs.Tracker().List(secretsGVR, schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, testStateNamespace)
		if err != nil {
			return true, nil, err
		}
		items := objs.(*corev1.SecretList).Items
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		idx, _ := strconv.Atoi(action.(k8stesting.ListActionImpl).ListOptions.Continue)
		page := &corev1.SecretList{Items: items[idx : idx+1]}
		if idx+1 < len(items) {
			page.Continue = strconv.Itoa(idx + 1)
		}
		return true, page, nil
	})
	all, err := store.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || calls != 3 {
		t.Fatalf("List paged %d calls, %d objects; want 3 and 3", calls, len(all))
	}
}

// TestK8sSecretStateStore_ErrorsCarryNoData: client-go errors are wrapped
// with the operation and object name only.
func TestK8sSecretStateStore_ErrorsCarryNoData(t *testing.T) {
	cs := newStateFakeClientset()
	store := newK8sSecretStateStore(cs, testStateNamespace)
	st := testState(testAtespaceA, "a")
	boom := apierrors.NewInternalError(errors.New("etcd says " + st.ControlToken))
	for _, verb := range []string{"create", "get", "update", "delete", "list"} {
		cs.PrependReactor(verb, "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, boom
		})
	}
	ctx := context.Background()
	st.version = "1"
	errs := []error{
		store.Create(ctx, st),
		func() error { _, err := store.Get(ctx, st.ID); return err }(),
		store.Update(ctx, st),
		store.Delete(ctx, st.ID, ""),
		func() error { _, err := store.List(ctx, ""); return err }(),
	}
	for i, err := range errs {
		if err == nil {
			t.Fatalf("op %d: expected an error", i)
		}
		if strings.Contains(err.Error(), st.ControlToken) || strings.Contains(err.Error(), "secret-a") {
			t.Errorf("op %d error leaks data: %v", i, err)
		}
	}
}

func TestK8sSecretStateStore_UnconfiguredFails(t *testing.T) {
	store := newK8sSecretStateStore(newStateFakeClientset(), "")
	if err := store.Create(context.Background(), testState(testAtespaceA, "a")); err == nil {
		t.Fatal("Create with no namespace must fail")
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer for log capture.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureRuntimeLog redirects runtimeLog (every level) to a buffer for the
// rest of the test.
func captureRuntimeLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := runtimeLog
	runtimeLog = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { runtimeLog = prev })
	return buf
}

// seedLegacyActor registers an actor directly with the fake ateapi client,
// with no state object: an actor created by a broker version without state
// persistence (a record-less, legacy actor). It returns the actor's id.
func seedLegacyActor(fc *fakeControlClient, projectID, actorName, uid string) string {
	atespace := substrateAtespaceName(projectID)
	id := atespace + "/" + actorName
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.createdActors[id] = &ateapipb.Actor{
		Metadata: &ateapipb.ResourceMetadata{Atespace: atespace, Name: actorName, Uid: uid},
		Status:   &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING},
	}
	return id
}
