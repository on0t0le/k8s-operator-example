/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apertureiov1alpha1 "github.com/on0t0le/k8s-operator-example/api/v1alpha1"
)

const finalizer = "aperture.io/application_controller_finalizer"

// ApplicationReconciler reconciles a Application object
type ApplicationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=aperture.io,resources=applications,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=aperture.io,resources=applications/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=aperture.io,resources=applications/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Application object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *ApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	var app apertureiov1alpha1.Application
	//retrieves the details of the object being managed
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		l.Error(err, "unable to fetch Application")
		return ctrl.Result{}, err
	}
	/*
	The finalizer is essential because it tells K8s we need control over object deletion.
	After all, how we will create other resources must be excluded together.
	Without the finalizer, there is no time for the K8s garbage collector to delete,
	and we risk having useless resources in the cluster.
	*/
	if !controllerutil.ContainsFinalizer(&app, finalizer) {
		l.Info("Adding Finalizer")
		controllerutil.AddFinalizer(&app, finalizer)
		return ctrl.Result{}, r.Update(ctx, &app)
	}

	if !app.DeletionTimestamp.IsZero() {
		l.Info("Application is being deleted")
		return r.reconcileDelete(ctx, &app)
	}
	l.Info("Application is being created")
	return r.reconcileCreate(ctx, &app)
}

func (r *ApplicationReconciler) reconcileCreate(ctx context.Context, app *apertureiov1alpha1.Application) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	l.Info("Creating deployment")
	err := r.createOrUpdateDeployment(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	l.Info("Creating service")
	err = r.createService(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ApplicationReconciler) createOrUpdateDeployment(ctx context.Context, app *apertureiov1alpha1.Application) error {
	var depl appsv1.Deployment
	deplName := types.NamespacedName{Name: app.ObjectMeta.Name + "-deployment", Namespace: app.ObjectMeta.Name}
	if err := r.Get(ctx, deplName, &depl); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("unable to fetch Deployment: %v", err)
		}
		/*If there is no Deployment, we will create it.
		An essential section in the definition is OwnerReferences, as it indicates to k8s that
		an Application is creating this resource.
		This is how k8s knows that when we remove an Application, it must also remove
		all the resources it created.
		Another important detail is that we use data from our Application to create the Deployment,
		such as image information, port, and replicas.
		*/
		if apierrors.IsNotFound(err) {
			depl = appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:        app.ObjectMeta.Name + "-deployment",
					Namespace:   app.ObjectMeta.Name,
					Labels:      map[string]string{"label": app.ObjectMeta.Name, "app": app.ObjectMeta.Name},
					Annotations: map[string]string{"imageregistry": "https://hub.docker.com/"},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: app.APIVersion,
							Kind:       app.Kind,
							Name:       app.Name,
							UID:        app.UID,
						},
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &app.Spec.Replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"label": app.ObjectMeta.Name},
					},
					Template: v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"label": app.ObjectMeta.Name, "app": app.ObjectMeta.Name},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name:  app.ObjectMeta.Name + "-container",
									Image: app.Spec.Image,
									Ports: []v1.ContainerPort{
										{
											ContainerPort: app.Spec.Port,
										},
									},
								},
							},
						},
					},
				},
			}
			err = r.Create(ctx, &depl)
			if err != nil {
				return fmt.Errorf("unable to create Deployment: %v", err)
			}
			return nil
		}
	}
	/*The controller also needs to manage the update because if the dev changes any information
	in an existing Application, this must impact other resources.*/
	depl.Spec.Replicas = &app.Spec.Replicas
	depl.Spec.Template.Spec.Containers[0].Image = app.Spec.Image
	depl.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort = app.Spec.Port
	err := r.Update(ctx, &depl)
	if err != nil {
		return fmt.Errorf("unable to update Deployment: %v", err)
	}
	return nil
}

func (r *ApplicationReconciler) createService(ctx context.Context, app *apertureiov1alpha1.Application) error {
	srv := v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.ObjectMeta.Name + "-service",
			Namespace: app.ObjectMeta.Name,
			Labels:    map[string]string{"app": app.ObjectMeta.Name},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: app.APIVersion,
					Kind:       app.Kind,
					Name:       app.Name,
					UID:        app.UID,
				},
			},
		},
		Spec: v1.ServiceSpec{
			Type:                  v1.ServiceTypeNodePort,
			ExternalTrafficPolicy: v1.ServiceExternalTrafficPolicyTypeLocal,
			Selector:              map[string]string{"app": app.ObjectMeta.Name},
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Port:       app.Spec.Port,
					Protocol:   v1.ProtocolTCP,
					TargetPort: intstr.FromInt(int(app.Spec.Port)),
				},
			},
		},
		Status: v1.ServiceStatus{},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, &srv, func() error {
		return nil
	})
	if err != nil {
		return fmt.Errorf("unable to create Service: %v", err)
	}
	return nil
}

func (r *ApplicationReconciler) reconcileDelete(ctx context.Context, app *apertureiov1alpha1.Application) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	l.Info("removing application")

	controllerutil.RemoveFinalizer(app, finalizer)
	err := r.Update(ctx, app)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("Error removing finalizer %v", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apertureiov1alpha1.Application{}).
		Complete(r)
}
