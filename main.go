package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/lrascao/kube-plex/pkg/signals"
)

const (
	constDefaultLimitCPU = "100m"
)

var (
	// data pvc name
	dataPVC = os.Getenv("DATA_PVC")

	// config pvc name
	configPVC = os.Getenv("CONFIG_PVC")

	// transcode pvc name
	transcodePVC = os.Getenv("TRANSCODE_PVC")

	// pms namespace
	namespace = os.Getenv("KUBE_NAMESPACE")

	// image for the plexmediaserver container containing the transcoder. This
	// should be set to the same as the 'master' pms server
	pmsImage           = os.Getenv("PMS_IMAGE")
	pmsInternalAddress = os.Getenv("PMS_INTERNAL_ADDRESS")

	// CPU limit
	limitCPU = os.Getenv("LIMIT_CPU")
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env := os.Environ()
	args := os.Args

	rewriteEnv(env)
	rewriteArgs(args)

	setDefaults()

	// uncomment below to debug ffmpeg args
	// fmt.Printf("%s\n", args)
	// args = []string{"/bin/sh", "-c", "sleep infinity"}

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Error getting working directory: %s", err)
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		log.Fatalf("Error building kubeconfig: %s", err)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("Error building kubernetes clientset: %s", err)
	}

	uid := os.Getenv("PLEX_UID")
	gid := os.Getenv("PLEX_GID")

	pod := generatePod(cwd, uid, gid, env, args)

	pod, err = kubeClient.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		log.Fatalf("Error creating pod: %s", err)
	}
	log.Printf("started pod %s\n", pod.Name)

	stopCh := signals.SetupSignalHandler()
	waitFn := func() <-chan error {
		stopCh := make(chan error)
		go func() {
			stopCh <- waitForPodCompletion(ctx, kubeClient, pod)
		}()
		return stopCh
	}

	select {
	case <-time.After(10 * time.Minute):
		log.Printf("timeout waiting for pod to complete")
	case err := <-waitFn():
		if err != nil {
			log.Printf("error waiting for pod to complete: %s", err)

			// dump pod logs
			req := kubeClient.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
			logsReader, err := req.Stream(ctx)
			if err != nil {
				log.Fatalf("Error getting pod logs: %s", err)
			}
			defer logsReader.Close()
			// read all logs and print them
			logs, err := io.ReadAll(logsReader)
			if err != nil {
				log.Fatalf("Error reading pod logs: %s", err)
			}
			log.Printf("pod logs:\n%s", logs)
		}
	case <-stopCh:
		log.Printf("exit requested.")
	}

	log.Printf("cleaning up pod...")
	if err := kubeClient.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
		log.Fatalf("error cleaning up pod: %s", err)
	}
}

// rewriteEnv rewrites environment variables to be passed to the transcoder
func rewriteEnv(in []string) {
	// no changes needed
}

func rewriteArgs(in []string) {
	for i, v := range in {
		switch v {
		case "-progressurl", "-manifest_name", "-segment_list":
			in[i+1] = strings.Replace(in[i+1], "http://127.0.0.1:32400", pmsInternalAddress, 1)
		case "-loglevel", "-loglevel_plex":
			in[i+1] = "debug"
		}
	}
}

func generatePod(cwd string, uid, gid string, env []string, args []string) *corev1.Pod {
	strToi64 := func(s string) *int64 {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			panic(err)
		}
		return &n
	}

	envVars := toCoreV1EnvVar(env)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pms-elastic-transcoder-",
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"kubernetes.io/arch": "amd64",
			},
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:  strToi64(uid),
				RunAsGroup: strToi64(gid),
			},
			Containers: []corev1.Container{
				{
					Name:       "plex",
					Command:    args,
					Image:      pmsImage,
					Env:        envVars,
					WorkingDir: cwd,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse(limitCPU),
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "data",
							MountPath: "/data",
						},
						{
							Name:      "config",
							MountPath: "/config",
							ReadOnly:  true,
						},
						{
							Name:      "transcode",
							MountPath: "/transcode",
						},
						{
							Name:      "transcode",
							MountPath: "/tmp",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: dataPVC,
						},
					},
				},
				{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: configPVC,
						},
					},
				},
				{
					Name: "transcode",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: transcodePVC,
						},
					},
				},
			},
		},
	}
}

func toCoreV1EnvVar(in []string) []corev1.EnvVar {
	out := make([]corev1.EnvVar, len(in))
	for i, v := range in {
		splitvar := strings.SplitN(v, "=", 2)
		out[i] = corev1.EnvVar{
			Name:  splitvar[0],
			Value: splitvar[1],
		}
	}
	return out
}

func waitForPodCompletion(ctx context.Context, cl kubernetes.Interface, pod *corev1.Pod) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled")
		case <-time.After(5 * time.Second):
			pod, err := cl.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}

			switch pod.Status.Phase {
			case corev1.PodPending:
			case corev1.PodRunning:
			case corev1.PodUnknown:
				log.Printf("warning: pod %q is in an unknown state", pod.Name)
			case corev1.PodFailed:
				return fmt.Errorf("pod %q failed", pod.Name)
			case corev1.PodSucceeded:
				return nil
			}
		}
	}
}

func setDefaults() {
	if limitCPU == "" {
		limitCPU = constDefaultLimitCPU
	}
}
