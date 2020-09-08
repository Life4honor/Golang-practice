package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func createNodeAffinity(node string) apiv1.Affinity {
	return apiv1.Affinity{
		NodeAffinity: &apiv1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &apiv1.NodeSelector{
				NodeSelectorTerms: []apiv1.NodeSelectorTerm{
					apiv1.NodeSelectorTerm{
						MatchExpressions: []apiv1.NodeSelectorRequirement{
							apiv1.NodeSelectorRequirement{
								Key:      "kubernetes.io/hostname",
								Operator: "In",
								Values:   []string{node},
							},
						},
					},
				},
			},
		},
	}
}

func buildVolume(name, pvcName string) apiv1.Volume {
	return apiv1.Volume{
		Name: name,
		VolumeSource: apiv1.VolumeSource{
			PersistentVolumeClaim: &apiv1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
				ReadOnly:  false,
			},
		},
	}
}

func buildServerContainer(name, image string, cmd []string) apiv1.Container {
	return apiv1.Container{
		Name:    name,
		Image:   image,
		Command: cmd,
	}
}

func buildClientContainer(name, image, volumeName string, cmd []string) apiv1.Container {
	return apiv1.Container{
		Name:    name,
		Image:   image,
		Command: cmd,
		VolumeMounts: []apiv1.VolumeMount{
			apiv1.VolumeMount{
				Name:      volumeName,
				MountPath: "/opt/common",
			},
		},
	}
}

func buildPod(podname, image, node string, cmd []string) apiv1.Pod {
	if node == "localhost" {
		node = "node1"
	}

	nodeAffinity := createNodeAffinity(node)
	s := buildServerContainer(podname, image, cmd)

	return apiv1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        podname,
			Labels:      map[string]string{"app": podname},
			Annotations: map[string]string{"sidecar.istio.io/inject": "false"},
		},
		Spec: apiv1.PodSpec{
			Containers: []apiv1.Container{
				s,
			},
			Affinity:      &nodeAffinity,
			RestartPolicy: apiv1.RestartPolicyNever,
		},
	}
}

func buildService(svcname, podname, protocol string, port int32) apiv1.Service {
	protocolMap := map[string]apiv1.Protocol{"tcp": apiv1.ProtocolTCP, "udp": apiv1.ProtocolUDP}

	return apiv1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: svcname,
			Labels: map[string]string{
				"app": podname,
			},
		},
		Spec: apiv1.ServiceSpec{
			Ports: []apiv1.ServicePort{
				apiv1.ServicePort{
					Name:     "tcp",
					Protocol: protocolMap[protocol],
					Port:     port,
				},
			},
			Selector: map[string]string{
				"app": podname,
			},
		},
	}
}

func buildPVC(name, scn string, rr string) apiv1.PersistentVolumeClaim {
	quantity, err := resource.ParseQuantity(rr)
	if err != nil {
		panic(err.Error())
	}

	return apiv1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PersistentVolumeClaim",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiv1.PersistentVolumeClaimSpec{
			Resources: apiv1.ResourceRequirements{
				Requests: apiv1.ResourceList{
					"storage": quantity,
				},
			},
			AccessModes: []apiv1.PersistentVolumeAccessMode{
				apiv1.ReadWriteOnce,
			},
			StorageClassName: &scn,
		},
	}
}

func buildJob(pvc *apiv1.PersistentVolumeClaim, v *apiv1.Volume, image, protocol, server, node, tag string, bl int32) batchv1.Job {
	jobName := "benchmark-" + protocol + "-" + node
	if node == "localhost" {
		node = "node1"
	}
	nodeAffinity := createNodeAffinity(node)
	opts := map[string][]string{
		"tcp": []string{},
		"udp": []string{"-b", "0", "--udp"},
	}
	cmd := []string{
		"iperf3",
		"-c",
		server,
		"-t",
		"10",
		"-i",
		"1",
		"--json",
		"--logfile",
		"/opt/common/iperf-" + tag + "-" + protocol + ".json",
	}
	cmd = append(cmd, opts[protocol]...)
	c := buildClientContainer(jobName, image, v.Name, cmd)
	containers := []apiv1.Container{}

	if server == "localhost" {
		s := buildServerContainer(jobName+"-server", image, []string{"iperf3", "-s", "-1"})
		containers = append(containers, s)
	}
	containers = append(containers, c)

	return batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Job",
			APIVersion: "batch/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: jobName,
		},
		Spec: batchv1.JobSpec{
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: jobName,
					Labels: map[string]string{
						"app": jobName,
					},
					Annotations: map[string]string{"sidecar.istio.io/inject": "false"},
				},
				Spec: apiv1.PodSpec{
					Containers:    containers,
					RestartPolicy: apiv1.RestartPolicyNever,
					Affinity:      &nodeAffinity,
					Volumes:       []apiv1.Volume{*v},
				},
			},
			BackoffLimit: &bl,
		},
	}
}

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	imagePtr := flag.String("image", "life4honor/benchmark", "benchmark docker image")
	namePtr := flag.String("name", "benchmark", "benchmark job name")
	nodePtr := flag.String("node", "node1", "client host")
	modePtr := flag.String("mode", "run", "mode [init (i), run (r), clean (c)]")
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	ctx := context.TODO()
	createOpts := metav1.CreateOptions{}
	deleteOpts := metav1.DeleteOptions{}

	for _, protocol := range []string{"tcp", "udp"} {
		svcname, podname, protocol, port := *namePtr+"-"+protocol, *namePtr, protocol, int32(5201)
		svc := buildService(svcname, podname, protocol, port)
		svcClient := clientset.CoreV1().Services("default")

		switch *modePtr {
		case "c", "clean":
			err := svcClient.Delete(ctx, svc.GetName(), deleteOpts)
			if err != nil {
				panic(err.Error())
			}
			fmt.Println("Deleted SVC successfully", svc.GetName())
		case "i", "init":
			svcResult, err := svcClient.Create(ctx, &svc, createOpts)
			if err != nil {
				panic(err.Error())
			}
			fmt.Println("Created SVC:", svcResult.Name)
		}
	}

	server := buildPod(*namePtr, *imagePtr, "node1", []string{"iperf3", "-s"})
	podClient := clientset.CoreV1().Pods("default")
	switch *modePtr {
	case "c", "clean":
		err := podClient.Delete(ctx, server.GetName(), deleteOpts)
		if err != nil {
			panic(err.Error())
		}
		fmt.Println("Deleted POD successfully", server.GetName())
	case "i", "init":
		podResult, err := podClient.Create(ctx, &server, createOpts)
		if err != nil {
			panic(err.Error())
		}
		fmt.Println("Created POD:", podResult.Name)
	}

	pvcClient := clientset.CoreV1().PersistentVolumeClaims("default")
	pvcName := *namePtr + "-" + *nodePtr
	pvc := buildPVC(pvcName, "local-path", "1Gi")

	pvcResult, err := pvcClient.Create(ctx, &pvc, createOpts)
	if err != nil {
		panic(err.Error())
	}
	fmt.Println("Created PVC:", pvcResult.Name)

	volumeName := pvcName
	v := buildVolume(volumeName, pvc.GetName())

	for _, protocol := range []string{"tcp", "udp"} {
		bl := int32(4)
		tags := map[string]string{
			"localhost": "pod",
			"node1":     "node",
			"node2":     "cluster",
			"node3":     "cluster",
		}
		server := *namePtr + "-" + protocol
		if *nodePtr == "localhost" {
			server = "localhost"
		}

		job := buildJob(&pvc, &v, *imagePtr, protocol, server, *nodePtr, tags[*nodePtr], bl)
		jobClient := clientset.BatchV1().Jobs("default")

		switch *modePtr {
		case "r", "run":
			jobResult, err := jobClient.Create(ctx, &job, createOpts)
			if err != nil {
				panic(err.Error())
			}
			fmt.Println("Created JOB:", jobResult.Name)

			for job, err := jobClient.Get(ctx, job.GetName(), metav1.GetOptions{}); job.Status.Succeeded == 0; {
				if err != nil {
					panic(err.Error())
				}
				fmt.Println("Job is still running", job.GetName())
				time.Sleep(time.Second * 2)
				job, err = jobClient.Get(ctx, job.GetName(), metav1.GetOptions{})
			}
		}
	}
}
