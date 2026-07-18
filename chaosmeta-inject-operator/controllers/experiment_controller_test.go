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

package controllers

import (
	"context"
	"fmt"
	"github.com/agiledragon/gomonkey"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/traas-stack/chaosmeta/chaosmeta-inject-operator/api/v1alpha1"
	mockscopehandler "github.com/traas-stack/chaosmeta/chaosmeta-inject-operator/mock/scopehandler"
	"github.com/traas-stack/chaosmeta/chaosmeta-inject-operator/pkg/model"
	"github.com/traas-stack/chaosmeta/chaosmeta-inject-operator/pkg/scopehandler"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strconv"
	"testing"
)

func Test_solveRange(t *testing.T) {
	testCount := 5
	var testObjectList []model.AtomicObject
	for i := 0; i < testCount; i++ {
		testObjectList = append(testObjectList, &model.PodObject{
			Namespace: "ns2",
			PodName:   fmt.Sprintf("pod%d", i),
		})
		testObjectList = append(testObjectList, &model.PodObject{
			Namespace: "ns1",
			PodName:   fmt.Sprintf("pod%d", i),
		})
	}

	type args struct {
		initial   []model.AtomicObject
		rangeMode *v1alpha1.RangeMode
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "all success",
			args: args{
				initial: testObjectList,
				rangeMode: &v1alpha1.RangeMode{
					Type: v1alpha1.AllRangeType,
				},
			},
			want: testCount * 2,
		},
		{
			name: "count success",
			args: args{
				initial: testObjectList,
				rangeMode: &v1alpha1.RangeMode{
					Type:  v1alpha1.CountRangeType,
					Value: 3,
				},
			},
			want: 3,
		},
		{
			name: "count more then initial length",
			args: args{
				initial: testObjectList,
				rangeMode: &v1alpha1.RangeMode{
					Type:  v1alpha1.CountRangeType,
					Value: 15,
				},
			},
			want: 10,
		},
		{
			name: "percent success",
			args: args{
				initial: testObjectList,
				rangeMode: &v1alpha1.RangeMode{
					Type:  v1alpha1.PercentRangeType,
					Value: 65,
				},
			},
			want: 6,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := solveRange(tt.args.initial, tt.args.rangeMode)
			if len(got) != tt.want {
				t.Errorf("solveRange() = %v, want %v", len(got), tt.want)
			}
		})
	}
}

func Test_initProcess(t *testing.T) {
	var (
		ctrl = gomock.NewController(t)
		ctx  = context.Background()
		exp  = &v1alpha1.Experiment{
			Spec: v1alpha1.ExperimentSpec{
				Scope: v1alpha1.PodScopeType,
				RangeMode: &v1alpha1.RangeMode{
					Type:  v1alpha1.CountRangeType,
					Value: 3,
				},
				Experiment: &v1alpha1.ExperimentCommon{
					Duration: "2m",
					Target:   "cpu",
					Fault:    "burn",
					Args: []v1alpha1.ArgsUnit{
						{
							Key:       "percent",
							Value:     "90",
							ValueType: v1alpha1.IntVType,
						},
						{
							Key:   v1alpha1.ContainerKey,
							Value: "nginx",
						},
					},
				},
				Selector: []v1alpha1.SelectorUnit{
					{
						Namespace: "chaosmeta",
					},
				},
				TargetPhase: v1alpha1.InjectPhaseType,
			},
			Status: v1alpha1.ExperimentStatus{},
		}
	)

	var reObject []model.AtomicObject
	reObject = append(reObject, &model.PodObject{
		Namespace:        "chaosmeta",
		PodName:          "chaosmeta-0",
		PodUID:           "d32tg32",
		PodIP:            "1.2.3.4",
		NodeName:         "node-1",
		NodeIP:           "2.2.2.2",
		ContainerID:      "g3g3g",
		ContainerRuntime: "docker",
	})
	defer ctrl.Finish()
	scopeHandlerMock := mockscopehandler.NewMockScopeHandler(ctrl)
	scopeHandlerMock.EXPECT().ConvertSelector(ctx, &exp.Spec).Return(reObject, nil)

	gomonkey.ApplyFunc(scopehandler.GetScopeHandler, func(v1alpha1.ScopeType) scopehandler.ScopeHandler {
		return scopeHandlerMock
	})

	initProcess(ctx, exp)
	assert.Equal(t, "pod/chaosmeta/chaosmeta-0", exp.Status.Detail.Inject[0].InjectObjectName)
	assert.Equal(t, v1alpha1.CreatedStatusType, exp.Status.Detail.Inject[0].Status)
	assert.Equal(t, v1alpha1.CreatedStatusType, exp.Status.Status)
	assert.Equal(t, v1alpha1.InjectPhaseType, exp.Status.Phase)

	scopeHandlerMock.EXPECT().ConvertSelector(ctx, &exp.Spec).Return([]model.AtomicObject{}, nil)
	initProcess(ctx, exp)
	assert.Equal(t, v1alpha1.FailedStatusType, exp.Status.Status)
}

func Test_solveFinalizer(t *testing.T) {
	instance := &v1alpha1.Experiment{
		ObjectMeta: metav1.ObjectMeta{
			Finalizers: []string{"awbgrewga", v1alpha1.FinalizerName, "shbertbhersth"},
		},
		Status: v1alpha1.ExperimentStatus{
			Phase:  v1alpha1.RecoverPhaseType,
			Status: v1alpha1.SuccessStatusType,
		},
	}
	solveFinalizer(instance)
	assert.Equal(t, []string{"awbgrewga", "shbertbhersth"}, instance.ObjectMeta.Finalizers)

	instance.ObjectMeta.Finalizers = []string{v1alpha1.FinalizerName, "shbertbhersth"}
	solveFinalizer(instance)
	assert.Equal(t, []string{"shbertbhersth"}, instance.ObjectMeta.Finalizers)

	instance.ObjectMeta.Finalizers = []string{"awbgrewga", v1alpha1.FinalizerName}
	solveFinalizer(instance)
	assert.Equal(t, []string{"awbgrewga"}, instance.ObjectMeta.Finalizers)

	instance.ObjectMeta.Finalizers = []string{v1alpha1.FinalizerName}
	solveFinalizer(instance)
	assert.Equal(t, []string{}, instance.ObjectMeta.Finalizers)
}

// Test_incrementRecoverRetry guards the MaxRecoverRetry escalation counter used by solveDeletion
// (D3/D4). The counter must (a) start at 1 on a fresh CR, (b) monotonically increase across
// successive reconcile retries, (c) persist as a string annotation that survives re-read, and
// (d) tolerate a corrupt / non-numeric prior value by resetting from 1.
//
// SCOPE NOTE: this only unit-tests the counter primitive. The control-flow defects documented in
// docs/fault-injection-enhance-test-report.md §2.A (solveDeletion's early-return severs the only
// path that actually issues node recover — statusProcess) and §2.B (new statuses have no case in
// statusProcess's switch) are NOT covered here: reproducing them needs an envtest integration test,
// which this machine cannot run (setup-envtest not installed; CRD yaml not regenerated for the new
// statuses because controller-gen crashes on Go 1.26). Those remain static-control-flow findings.
func Test_incrementRecoverRetry(t *testing.T) {
	// Fresh CR: no annotation yet -> first retry == 1, annotation persisted.
	inst := &v1alpha1.Experiment{ObjectMeta: metav1.ObjectMeta{Annotations: nil}}
	n := incrementRecoverRetry(inst)
	assert.Equal(t, 1, n, "first retry should be 1")
	assert.Equal(t, "1", inst.ObjectMeta.Annotations[recoverRetryKey], "annotation persisted as %q")

	// Three more retries -> 2, 3, 4, monotonically, re-reading the persisted value each time.
	for want := 2; want <= 4; want++ {
		got := incrementRecoverRetry(inst)
		assert.Equal(t, want, got, "retry should monotonically increment to %d", want)
		assert.Equal(t, strconv.Itoa(want), inst.ObjectMeta.Annotations[recoverRetryKey])
	}

	// Tolerate a corrupt (non-numeric) prior annotation: reset to 1, not panic.
	inst2 := &v1alpha1.Experiment{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{recoverRetryKey: "garbage"},
	}}
	got := incrementRecoverRetry(inst2)
	assert.Equal(t, 1, got, "corrupt prior value should reset to 1")
	assert.Equal(t, "1", inst2.ObjectMeta.Annotations[recoverRetryKey])

	// A valid high prior value is honoured: 7 -> 8 (the MaxRecoverRetry boundary).
	inst3 := &v1alpha1.Experiment{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{recoverRetryKey: "7"},
	}}
	got = incrementRecoverRetry(inst3)
	assert.Equal(t, 8, got, "valid prior 7 should bump to 8 (MaxRecoverRetry boundary)")
	assert.Equal(t, "8", inst3.ObjectMeta.Annotations[recoverRetryKey])
}
