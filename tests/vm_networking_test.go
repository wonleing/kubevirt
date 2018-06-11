/*
 * This file is part of the kubevirt project
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
 *
 * Copyright 2018 Red Hat, Inc.
 *
 */

package tests_test

import (
	"flag"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/google/goexpect"

	"fmt"

	v12 "k8s.io/api/core/v1"
	v13 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onsi/ginkgo/extensions/table"

	"k8s.io/apimachinery/pkg/util/intstr"

	"kubevirt.io/kubevirt/pkg/api/v1"
	"kubevirt.io/kubevirt/pkg/kubecli"
	"kubevirt.io/kubevirt/pkg/log"
	"kubevirt.io/kubevirt/tests"
)

var _ = Describe("Networking", func() {

	flag.Parse()

	virtClient, err := kubecli.GetKubevirtClient()
	tests.PanicOnError(err)

	var inboundVMI *v1.VirtualMachineInstance
	var outboundVMI *v1.VirtualMachineInstance

	// newHelloWorldJob takes a dns entry or an IP which it will use create a pod
	// which tries to contact the host on port 1500. It expects to receive "Hello World!" to succeed.
	newHelloWorldJob := func(host string) *v12.Pod {
		check := []string{fmt.Sprintf(`set -x; x="$(head -n 1 < <(nc %s 1500 -i 1 -w 1))"; echo "$x" ; if [ "$x" = "Hello World!" ]; then echo "succeeded"; exit 0; else echo "failed"; exit 1; fi`, host)}
		job := tests.RenderJob("netcat", []string{"/bin/bash", "-c"}, check)

		return job
	}

	logPodLogs := func(pod *v12.Pod) {
		defer GinkgoRecover()

		var s int64 = 500
		logs := virtClient.CoreV1().Pods(inboundVMI.Namespace).GetLogs(pod.Name, &v12.PodLogOptions{SinceSeconds: &s})
		rawLogs, err := logs.DoRaw()
		Expect(err).ToNot(HaveOccurred())
		log.Log.Infof("%v", rawLogs)
	}

	waitForPodToFinish := func(pod *v12.Pod) v12.PodPhase {
		Eventually(func() v12.PodPhase {
			j, err := virtClient.Core().Pods(inboundVMI.ObjectMeta.Namespace).Get(pod.ObjectMeta.Name, v13.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			return j.Status.Phase
		}, 30*time.Second, 1*time.Second).Should(Or(Equal(v12.PodSucceeded), Equal(v12.PodFailed)))
		j, err := virtClient.Core().Pods(inboundVMI.ObjectMeta.Namespace).Get(pod.ObjectMeta.Name, v13.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		logPodLogs(pod)
		return j.Status.Phase
	}

	waitUntilVmReady := func(vm *v1.VirtualMachineInstance, expecterFactory tests.VmExpecterFactory) {
		// Wait for VirtualMachineInstance start
		tests.WaitForSuccessfulVMIStart(vm)

		// Fetch the new VirtualMachineInstance with updated status
		vm, err := virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Get(vm.Name, v13.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Lets make sure that the OS is up by waiting until we can login
		expecter, err := expecterFactory(vm)
		Expect(err).ToNot(HaveOccurred())
		expecter.Close()
	}

	// TODO this is not optimal, since the one test which will initiate this, will look slow
	tests.BeforeAll(func() {
		tests.BeforeTestCleanup()

		// Create and start inbound VirtualMachineInstance
		inboundVMI = tests.NewRandomVMIWithEphemeralDiskAndUserdata(tests.RegistryDiskFor(tests.RegistryDiskCirros), "#!/bin/bash\necho 'hello'\n")
		inboundVMI.Labels = map[string]string{"expose": "me"}
		inboundVMI.Spec.Subdomain = "myvm"
		inboundVMI.Spec.Hostname = "my-subdomain"
		_, err = virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Create(inboundVMI)
		Expect(err).ToNot(HaveOccurred())

		// Create and start outbound VirtualMachineInstance
		outboundVMI = tests.NewRandomVMIWithEphemeralDiskAndUserdata(tests.RegistryDiskFor(tests.RegistryDiskCirros), "#!/bin/bash\necho 'hello'\n")
		_, err = virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Create(outboundVMI)
		Expect(err).ToNot(HaveOccurred())

		for _, networkVm := range []*v1.VirtualMachineInstance{inboundVMI, outboundVMI} {
			waitUntilVmReady(networkVm, tests.LoggedInCirrosExpecter)
		}

		inboundVMI, err = virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Get(inboundVMI.Name, v13.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		expecter, _, err := tests.NewConsoleExpecter(virtClient, inboundVMI, 10*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer expecter.Close()
		resp, err := expecter.ExpectBatch([]expect.Batcher{
			&expect.BSnd{S: "\n"},
			&expect.BExp{R: "\\$ "},
			&expect.BSnd{S: "screen -d -m nc -klp 1500 -e echo -e \"Hello World!\"\n"},
			&expect.BExp{R: "\\$ "},
			&expect.BSnd{S: "echo $?\n"},
			&expect.BExp{R: "0"},
		}, 60*time.Second)
		log.DefaultLogger().Infof("%v", resp)
		Expect(err).ToNot(HaveOccurred())

		outboundVMI, err = virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Get(outboundVMI.Name, v13.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
	})

	Context("VirtualMachineInstance attached to the pod network", func() {

		table.DescribeTable("should be able to reach", func(destination string) {
			var cmdCheck, addrShow, addr string

			// assuming pod network is of standard MTU = 1500 (minus 50 bytes for vxlan overhead)
			expectedMtu := 1450
			ipHeaderSize := 28 // IPv4 specific
			payloadSize := expectedMtu - ipHeaderSize

			// Wait until the VirtualMachineInstance is booted, ping google and check if we can reach the internet
			expecter, _, err := tests.NewConsoleExpecter(virtClient, outboundVMI, 10*time.Second)
			defer expecter.Close()
			Expect(err).ToNot(HaveOccurred())

			switch destination {
			case "Internet":
				addr = "www.google.com"
			case "InboundVMI":
				addr = inboundVMI.Status.Interfaces[0].IP
			}

			By("checking br1 MTU inside the pod")
			vmPod := tests.GetRunningPodByLabel(outboundVMI.Name, v1.DomainLabel, tests.NamespaceTestDefault)
			output, err := tests.ExecuteCommandOnPod(
				virtClient,
				vmPod,
				vmPod.Spec.Containers[0].Name,
				[]string{"ip", "address", "show", "br1"},
			)
			log.Log.Infof("%v", output)
			Expect(err).ToNot(HaveOccurred())
			// the following substring is part of 'ip address show' output
			expectedMtuString := fmt.Sprintf("mtu %d", expectedMtu)
			Expect(strings.Contains(output, expectedMtuString)).To(BeTrue())

			By("checking eth0 MTU inside the VirtualMachineInstance")
			addrShow = "ip address show eth0\n"
			out, err := expecter.ExpectBatch([]expect.Batcher{
				&expect.BSnd{S: "\n"},
				&expect.BExp{R: "\\$ "},
				&expect.BSnd{S: addrShow},
				&expect.BExp{R: fmt.Sprintf(".*%s.*\n", expectedMtuString)},
				&expect.BSnd{S: "echo $?\n"},
				&expect.BExp{R: "0"},
			}, 180*time.Second)
			log.Log.Infof("%v", out)
			Expect(err).ToNot(HaveOccurred())

			By("checking the VirtualMachineInstance can send MTU sized frames to another VirtualMachineInstance")
			// NOTE: VirtualMachineInstance is not directly accessible from inside the pod because
			// we transferred its IP address under DHCP server control, so the
			// only thing we can validate is connectivity between VMIs
			//
			// NOTE: cirros ping doesn't support -M do that could be used to
			// validate end-to-end connectivity with Don't Fragment flag set
			cmdCheck = fmt.Sprintf("ping %s -c 1 -w 5 -s %d\n", addr, payloadSize)
			out, err = expecter.ExpectBatch([]expect.Batcher{
				&expect.BSnd{S: "\n"},
				&expect.BExp{R: "\\$ "},
				&expect.BSnd{S: cmdCheck},
				&expect.BExp{R: "\\$ "},
				&expect.BSnd{S: "echo $?\n"},
				&expect.BExp{R: "0"},
			}, 180*time.Second)
			log.Log.Infof("%v", out)
			Expect(err).ToNot(HaveOccurred())
		},
			table.Entry("the Inbound VirtualMachineInstance", "InboundVMI"),
			table.Entry("the internet", "Internet"),
		)

		table.DescribeTable("should be reachable via the propagated IP from a Pod", func(op v12.NodeSelectorOperator, hostNetwork bool) {

			ip := inboundVMI.Status.Interfaces[0].IP

			//TODO if node count 1, skip whe nv12.NodeSelectorOpOut
			nodes, err := virtClient.CoreV1().Nodes().List(v13.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(nodes.Items).ToNot(BeEmpty())
			if len(nodes.Items) == 1 && op == v12.NodeSelectorOpNotIn {
				Skip("Skip network test that requires multiple nodes when only one node is present.")
			}

			// Run netcat and give it one second to ghet "Hello World!" back from the VirtualMachineInstance
			job := newHelloWorldJob(ip)
			job.Spec.Affinity = &v12.Affinity{
				NodeAffinity: &v12.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v12.NodeSelector{
						NodeSelectorTerms: []v12.NodeSelectorTerm{
							{
								MatchExpressions: []v12.NodeSelectorRequirement{
									{Key: "kubernetes.io/hostname", Operator: op, Values: []string{inboundVMI.Status.NodeName}},
								},
							},
						},
					},
				},
			}
			job.Spec.HostNetwork = hostNetwork

			job, err = virtClient.CoreV1().Pods(inboundVMI.ObjectMeta.Namespace).Create(job)
			Expect(err).ToNot(HaveOccurred())
			phase := waitForPodToFinish(job)
			Expect(phase).To(Equal(v12.PodSucceeded))
		},
			table.Entry("on the same node from Pod", v12.NodeSelectorOpIn, false),
			table.Entry("on a different node from Pod", v12.NodeSelectorOpNotIn, false),
			table.Entry("on the same node from Node", v12.NodeSelectorOpIn, true),
			table.Entry("on a different node from Node", v12.NodeSelectorOpNotIn, true),
		)

		Context("with a service matching the vm exposed", func() {
			BeforeEach(func() {
				service := &v12.Service{
					ObjectMeta: v13.ObjectMeta{
						Name: "myservice",
					},
					Spec: v12.ServiceSpec{
						Selector: map[string]string{
							"expose": "me",
						},
						Ports: []v12.ServicePort{
							{Protocol: v12.ProtocolTCP, Port: 1500, TargetPort: intstr.FromInt(1500)},
						},
					},
				}

				_, err := virtClient.CoreV1().Services(inboundVMI.Namespace).Create(service)
				Expect(err).ToNot(HaveOccurred())

			})
			It(" should be able to reach the vm based on labels specified on the vm", func() {

				By("starting a pod which tries to reach the vm via the defined service")
				job := newHelloWorldJob(fmt.Sprintf("%s.%s", "myservice", inboundVMI.Namespace))
				job, err = virtClient.CoreV1().Pods(inboundVMI.Namespace).Create(job)
				Expect(err).ToNot(HaveOccurred())

				By("waiting for the pod to report a successful connection attempt")
				phase := waitForPodToFinish(job)
				Expect(phase).To(Equal(v12.PodSucceeded))
			})
			It("should fail to reach the vm if an invalid servicename is used", func() {

				By("starting a pod which tries to reach the vm via a non-existent service")
				job := newHelloWorldJob(fmt.Sprintf("%s.%s", "wrongservice", inboundVMI.Namespace))
				job, err = virtClient.CoreV1().Pods(inboundVMI.Namespace).Create(job)
				Expect(err).ToNot(HaveOccurred())
				By("waiting for the pod to report an  unsuccessful connection attempt")
				phase := waitForPodToFinish(job)
				Expect(phase).To(Equal(v12.PodFailed))
			})

			AfterEach(func() {
				Expect(virtClient.CoreV1().Services(inboundVMI.Namespace).Delete("myservice", &v13.DeleteOptions{})).To(Succeed())
			})
		})

		Context("with a subdomain and a headless service given", func() {
			BeforeEach(func() {
				service := &v12.Service{
					ObjectMeta: v13.ObjectMeta{
						Name: inboundVMI.Spec.Subdomain,
					},
					Spec: v12.ServiceSpec{
						ClusterIP: v12.ClusterIPNone,
						Selector: map[string]string{
							"expose": "me",
						},
						/* Normally ports are not required on headless services, but there is a bug in kubedns:
						https://github.com/kubernetes/kubernetes/issues/55158
						*/
						Ports: []v12.ServicePort{
							{Protocol: v12.ProtocolTCP, Port: 1500, TargetPort: intstr.FromInt(1500)},
						},
					},
				}
				_, err := virtClient.CoreV1().Services(inboundVMI.Namespace).Create(service)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should be able to reach the vm via its unique fully qualified domain name", func() {
				By("starting a pod which tries to reach the vm via the defined service")
				job := newHelloWorldJob(fmt.Sprintf("%s.%s.%s", inboundVMI.Spec.Hostname, inboundVMI.Spec.Subdomain, inboundVMI.Namespace))
				job, err = virtClient.CoreV1().Pods(inboundVMI.Namespace).Create(job)
				Expect(err).ToNot(HaveOccurred())

				By("waiting for the pod to report a successful connection attempt")
				phase := waitForPodToFinish(job)
				Expect(phase).To(Equal(v12.PodSucceeded))
			})

			AfterEach(func() {
				Expect(virtClient.CoreV1().Services(inboundVMI.Namespace).Delete(inboundVMI.Spec.Subdomain, &v13.DeleteOptions{})).To(Succeed())
			})
		})
	})

	checkNetworkVendor := func(vm *v1.VirtualMachineInstance, expectedVendor string, prompt string) {
		expecter, _, err := tests.NewConsoleExpecter(virtClient, vm, 10*time.Second)
		defer expecter.Close()
		Expect(err).ToNot(HaveOccurred())

		out, err := expecter.ExpectBatch([]expect.Batcher{
			&expect.BSnd{S: "\n"},
			&expect.BExp{R: prompt},
			&expect.BSnd{S: "cat /sys/class/net/eth0/device/vendor\n"},
			&expect.BExp{R: expectedVendor},
		}, 15*time.Second)
		log.Log.Infof("%v", out)
		Expect(err).ToNot(HaveOccurred())
	}

	Context("VirtualMachineInstance with custom interface model", func() {
		It("should expose the right device type to the guest", func() {
			By("checking the device vendor in /sys/class")
			// Create a machine with e1000 interface model
			e1000VMI := tests.NewRandomVMIWithe1000NetworkInterface()
			_, err = virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Create(e1000VMI)
			Expect(err).ToNot(HaveOccurred())

			waitUntilVmReady(e1000VMI, tests.LoggedInAlpineExpecter)
			// as defined in https://vendev.org/pci/ven_8086/
			checkNetworkVendor(e1000VMI, "0x8086", "localhost:~#")
		})
	})

	Context("VirtualMachineInstance with default interface model", func() {
		It("should expose the right device type to the guest", func() {
			By("checking the device vendor in /sys/class")
			for _, networkVm := range []*v1.VirtualMachineInstance{inboundVMI, outboundVMI} {
				// as defined in https://vendev.org/pci/ven_1af4/
				checkNetworkVendor(networkVm, "0x1af4", "\\$ ")
			}
		})
	})

})
