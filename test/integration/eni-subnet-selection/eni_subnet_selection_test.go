// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package eni_subnet_selection

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/aws/amazon-vpc-cni-k8s/test/framework/resources/k8s/manifest"
	"github.com/aws/amazon-vpc-cni-k8s/test/framework/utils"
	"github.com/aws/amazon-vpc-cni-k8s/test/integration/common"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/apps/v1"
)

var _ = Describe("ENI Subnet Selection Test", func() {
	var (
		deployment      *v1.Deployment
		podLabelKey     string
		podLabelVal     string
		err             error
		newEniSubnetIds []string
	)

	Context("when creating deployment", func() {
		BeforeEach(func() {
			podLabelKey = "role"
			podLabelVal = "eni-subnet-selection-test"
		})

		JustBeforeEach(func() {
			By("creating deployment")
			container := manifest.NewNetCatAlpineContainer(f.Options.TestImageRegistry).
				Command([]string{"sleep"}).
				Args([]string{"3600"}).
				Build()

			deploymentBuilder := manifest.NewBusyBoxDeploymentBuilder(f.Options.TestImageRegistry).
				Container(container).
				Replicas(50).
				PodLabel(podLabelKey, podLabelVal).
				NodeName(*primaryInstance.PrivateDnsName).
				Build()

			deployment, err = f.K8sResourceManagers.DeploymentManager().
				CreateAndWaitTillDeploymentIsReady(deploymentBuilder, utils.DefaultDeploymentReadyTimeout)
			Expect(err).ToNot(HaveOccurred())

			// Wait for deployment to settle, as if any pods restart, their pod IP will change between
			// the GET and the validation.
			time.Sleep(5 * time.Second)
		})

		JustAfterEach(func() {
			By("deleting deployment")
			err := f.K8sResourceManagers.DeploymentManager().DeleteAndWaitTillDeploymentIsDeleted(deployment)
			Expect(err).ToNot(HaveOccurred())

			By("sleeping to allow CNI Plugin to delete unused ENIs")
			time.Sleep(time.Second * 90)

			newEniSubnetIds = nil
		})

		Context("when using a tagged subnet with /18", func() {
			It(fmt.Sprintf("should have subnet in CIDR range %s", cidrRangeString), func() {
				instance, err := f.CloudServices.EC2().DescribeInstance(*primaryInstance.InstanceId)
				Expect(err).ToNot(HaveOccurred())

				By("retrieving secondary ENIs")
				for _, nwInterface := range instance.NetworkInterfaces {
					primaryENI := common.IsPrimaryENI(nwInterface, instance.PrivateIpAddress)
					if !primaryENI {
						newEniSubnetIds = append(newEniSubnetIds, *nwInterface.SubnetId)
					}
				}

				By("verifying at least one new Secondary ENI is created")
				Expect(len(newEniSubnetIds)).Should(BeNumerically(">", 0))

				expectedCidrSplit := strings.Split(cidrRangeString, "/")
				expectedSuffix, _ := strconv.Atoi(expectedCidrSplit[1])

				_, expectedCIDR, _ := net.ParseCIDR(cidrRangeString)

				By(fmt.Sprintf("checking the secondary ENI subnets are in the CIDR %s", cidrRangeString))
				for _, subnetID := range newEniSubnetIds {
					subnetOutput, err := f.CloudServices.EC2().DescribeSubnet(subnetID)
					Expect(err).ToNot(HaveOccurred())
					cidrSplit := strings.Split(*subnetOutput.Subnets[0].CidrBlock, "/")
					actualSubnetIp, _, _ := net.ParseCIDR(*subnetOutput.Subnets[0].CidrBlock)
					Expect(expectedCIDR.Contains(actualSubnetIp))
					suffix, _ := strconv.Atoi(cidrSplit[1])
					Expect(suffix).Should(BeNumerically(">=", expectedSuffix))
				}
			})
		})

		Context("when using an untagged subnet with /18", func() {
			BeforeEach(func() {
				By("Untagging the subnet")
				_, err = f.CloudServices.EC2().
					DeleteTags(
						[]string{createdSubnet},
						[]*ec2.Tag{
							{
								Key:   aws.String("kubernetes.io/role/cni"),
								Value: aws.String("1"),
							},
						},
					)
				Expect(err).ToNot(HaveOccurred())
			})
			It("should have the same subnets as the primary ENI", func() {
				instance, err := f.CloudServices.EC2().DescribeInstance(*primaryInstance.InstanceId)
				Expect(err).ToNot(HaveOccurred())

				By("retrieving secondary ENIs")
				for _, nwInterface := range instance.NetworkInterfaces {
					primaryENI := common.IsPrimaryENI(nwInterface, instance.PrivateIpAddress)
					if !primaryENI {
						newEniSubnetIds = append(newEniSubnetIds, *nwInterface.SubnetId)
					} else {

					}
				}

				By("verifying at least one new Secondary ENI is created")
				Expect(len(newEniSubnetIds)).Should(BeNumerically(">", 0))

				vpcOutput, err := f.CloudServices.EC2().DescribeVPC(*primaryInstance.VpcId)
				Expect(err).ToNot(HaveOccurred())

				expectedCidrSplit := strings.Split(*vpcOutput.Vpcs[0].CidrBlock, "/")
				expectedSuffix, _ := strconv.Atoi(expectedCidrSplit[1])
				_, expectedCIDR, _ := net.ParseCIDR(*vpcOutput.Vpcs[0].CidrBlock)

				By(fmt.Sprintf("checking the secondary ENI subnets are in the CIDR %s", *vpcOutput.Vpcs[0].CidrBlock))
				for _, subnetID := range newEniSubnetIds {
					subnetOutput, err := f.CloudServices.EC2().DescribeSubnet(subnetID)
					Expect(err).ToNot(HaveOccurred())
					cidrSplit := strings.Split(*subnetOutput.Subnets[0].CidrBlock, "/")
					actualSubnetIp, _, _ := net.ParseCIDR(*subnetOutput.Subnets[0].CidrBlock)
					Expect(expectedCIDR.Contains(actualSubnetIp))
					suffix, _ := strconv.Atoi(cidrSplit[1])
					Expect(suffix).Should(BeNumerically(">=", expectedSuffix))
				}
			})
		})
	})
})
