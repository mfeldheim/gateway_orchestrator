package gateway

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestPool_SelectGateway(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1.AddToScheme(scheme)

	tests := []struct {
		name             string
		existingGateways []gwapiv1.Gateway
		visibility       string
		selector         *metav1.LabelSelector
		wantGateway      string
		wantNil          bool
		wantErr          bool
	}{
		{
			name: "select gateway with capacity",
			existingGateways: []gwapiv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-01",
						Namespace: "edge",
						Annotations: map[string]string{
							"gateway.opendi.com/visibility":        "internet-facing",
							"gateway.opendi.com/certificate-count": "5",
							"gateway.opendi.com/rule-count":        "20",
						},
					},
					Spec: gwapiv1.GatewaySpec{
						GatewayClassName: "aws-alb",
					},
				},
			},
			visibility:  "internet-facing",
			selector:    nil,
			wantGateway: "gw-01",
			wantNil:     false,
		},
		{
			name: "no gateway with capacity",
			existingGateways: []gwapiv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-01",
						Namespace: "edge",
						Annotations: map[string]string{
							"gateway.opendi.com/visibility":        "internet-facing",
							"gateway.opendi.com/certificate-count": "20",  // At limit
							"gateway.opendi.com/rule-count":        "100", // At limit
						},
					},
					Spec: gwapiv1.GatewaySpec{
						GatewayClassName: "aws-alb",
					},
				},
			},
			visibility:  "internet-facing",
			selector:    nil,
			wantGateway: "",
			wantNil:     true,
		},
		{
			name: "visibility mismatch",
			existingGateways: []gwapiv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-01",
						Namespace: "edge",
						Annotations: map[string]string{
							"gateway.opendi.com/visibility":        "internal",
							"gateway.opendi.com/certificate-count": "5",
						},
					},
					Spec: gwapiv1.GatewaySpec{
						GatewayClassName: "aws-alb",
					},
				},
			},
			visibility:  "internet-facing",
			selector:    nil,
			wantGateway: "",
			wantNil:     true,
		},
		{
			name:             "no gateways exist",
			existingGateways: []gwapiv1.Gateway{},
			visibility:       "internet-facing",
			selector:         nil,
			wantGateway:      "",
			wantNil:          true,
		},
		{
			name: "multiple gateways - select first with capacity",
			existingGateways: []gwapiv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-01",
						Namespace: "edge",
						Annotations: map[string]string{
							"gateway.opendi.com/visibility":        "internet-facing",
							"gateway.opendi.com/certificate-count": "20", // Full
						},
					},
					Spec: gwapiv1.GatewaySpec{
						GatewayClassName: "aws-alb",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-02",
						Namespace: "edge",
						Annotations: map[string]string{
							"gateway.opendi.com/visibility":        "internet-facing",
							"gateway.opendi.com/certificate-count": "5", // Has capacity
						},
					},
					Spec: gwapiv1.GatewaySpec{
						GatewayClassName: "aws-alb",
					},
				},
			},
			visibility:  "internet-facing",
			selector:    nil,
			wantGateway: "gw-02",
			wantNil:     false,
		},
		{
			name: "select gateway matching label selector",
			existingGateways: []gwapiv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-01",
						Namespace: "edge",
						Labels: map[string]string{
							"tier": "free",
						},
						Annotations: map[string]string{
							"gateway.opendi.com/visibility":        "internet-facing",
							"gateway.opendi.com/certificate-count": "5",
						},
					},
					Spec: gwapiv1.GatewaySpec{
						GatewayClassName: "aws-alb",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-02",
						Namespace: "edge",
						Labels: map[string]string{
							"tier": "premium",
						},
						Annotations: map[string]string{
							"gateway.opendi.com/visibility":        "internet-facing",
							"gateway.opendi.com/certificate-count": "5",
						},
					},
					Spec: gwapiv1.GatewaySpec{
						GatewayClassName: "aws-alb",
					},
				},
			},
			visibility: "internet-facing",
			selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"tier": "premium"},
			},
			wantGateway: "gw-02",
			wantNil:     false,
		},
		{
			name: "no gateway matching label selector",
			existingGateways: []gwapiv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-01",
						Namespace: "edge",
						Labels: map[string]string{
							"tier": "free",
						},
						Annotations: map[string]string{
							"gateway.opendi.com/visibility":        "internet-facing",
							"gateway.opendi.com/certificate-count": "5",
						},
					},
					Spec: gwapiv1.GatewaySpec{
						GatewayClassName: "aws-alb",
					},
				},
			},
			visibility: "internet-facing",
			selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"tier": "premium"},
			},
			wantGateway: "",
			wantNil:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]runtime.Object, len(tt.existingGateways))
			for i := range tt.existingGateways {
				objs[i] = &tt.existingGateways[i]
			}

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				Build()

			pool := NewPool(client, "edge", "aws-alb")
			ctx := context.Background()

			got, err := pool.SelectGateway(ctx, tt.visibility, "", tt.selector)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SelectGateway() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got.Name)
				}
			} else {
				if got == nil {
					t.Fatal("expected gateway, got nil")
				}
				if got.Name != tt.wantGateway {
					t.Errorf("gateway name = %v, want %v", got.Name, tt.wantGateway)
				}
			}
		})
	}
}

func TestPool_GetNextGatewayIndex(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1.AddToScheme(scheme)

	tests := []struct {
		name             string
		existingGateways []gwapiv1.Gateway
		wantIndex        int
	}{
		{
			name:             "no gateways",
			existingGateways: []gwapiv1.Gateway{},
			wantIndex:        1,
		},
		{
			name: "one gateway",
			existingGateways: []gwapiv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-01",
						Namespace: "edge",
					},
				},
			},
			wantIndex: 2,
		},
		{
			name: "multiple gateways",
			existingGateways: []gwapiv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-01",
						Namespace: "edge",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-03",
						Namespace: "edge",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-02",
						Namespace: "edge",
					},
				},
			},
			wantIndex: 4,
		},
		{
			name: "mixed gateway names",
			existingGateways: []gwapiv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gw-05",
						Namespace: "edge",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-gateway",
						Namespace: "edge",
					},
				},
			},
			wantIndex: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]runtime.Object, len(tt.existingGateways))
			for i := range tt.existingGateways {
				objs[i] = &tt.existingGateways[i]
			}

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				Build()

			pool := NewPool(client, "edge", "aws-alb")
			ctx := context.Background()

			got, err := pool.GetNextGatewayIndex(ctx)
			if err != nil {
				t.Fatalf("GetNextGatewayIndex() error = %v", err)
			}

			if got != tt.wantIndex {
				t.Errorf("index = %v, want %v", got, tt.wantIndex)
			}
		})
	}
}

func TestPool_CreateGateway(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gwapiv1.AddToScheme(scheme)

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	pool := NewPool(client, "edge", "aws-alb")
	ctx := context.Background()

	tests := []struct {
		name       string
		visibility string
		index      int
	}{
		{
			name:       "create internet-facing gateway",
			visibility: "internet-facing",
			index:      1,
		},
		{
			name:       "create internal gateway",
			visibility: "internal",
			index:      2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := pool.CreateGateway(ctx, tt.visibility, "", tt.index)
			if err != nil {
				t.Fatalf("CreateGateway() error = %v", err)
			}

			if info == nil {
				t.Fatal("expected gateway info, got nil")
			}

			expectedName := "gw-01"
			if tt.index == 2 {
				expectedName = "gw-02"
			}

			if info.Name != expectedName {
				t.Errorf("name = %v, want %v", info.Name, expectedName)
			}

			// Verify gateway was created
			var gw gwapiv1.Gateway
			err = client.Get(ctx, types.NamespacedName{Name: info.Name, Namespace: "edge"}, &gw)
			if err != nil {
				t.Fatalf("gateway not created: %v", err)
			}

			// Verify annotations
			if gw.Annotations["gateway.opendi.com/visibility"] != tt.visibility {
				t.Errorf("visibility annotation = %v, want %v",
					gw.Annotations["gateway.opendi.com/visibility"], tt.visibility)
			}

			// Verify infrastructure.parametersRef
			if gw.Spec.Infrastructure == nil || gw.Spec.Infrastructure.ParametersRef == nil {
				t.Error("expected infrastructure.parametersRef to be set")
			} else {
				expectedConfigName := fmt.Sprintf("%s-config", info.Name)
				if gw.Spec.Infrastructure.ParametersRef.Name != expectedConfigName {
					t.Errorf("parametersRef.name = %v, want %v",
						gw.Spec.Infrastructure.ParametersRef.Name, expectedConfigName)
				}
				if gw.Spec.Infrastructure.ParametersRef.Kind != "LoadBalancerConfiguration" {
					t.Errorf("parametersRef.kind = %v, want LoadBalancerConfiguration",
						gw.Spec.Infrastructure.ParametersRef.Kind)
				}
			}

			// Verify listeners
			if len(gw.Spec.Listeners) != 2 {
				t.Errorf("listener count = %v, want 2", len(gw.Spec.Listeners))
			}
		})
	}
}
