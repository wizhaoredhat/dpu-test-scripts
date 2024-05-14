Mellanox Debugging

sudo podman run --pull always --replace --pid host --network host --user 0 --name bf -dit --privileged -v /dev:/dev quay.io/bnemeth/bf
sudo podman exec -it bf bash

mstflint -d 98:00.0 query
mstfwreset  -d 98:00.0 reset
mlxup

Must Gather

oc adm must-gather -- /usr/bin/gather_network_logs

Unbind VFs

echo -n "0000:81:00.2" > /sys/bus/pci/drivers/mlx5_core/unbind

Scapy Send IPv6 Multicast

ip addr add 2001:5c0:9168::2/64 dev ens4f0np0
ip addr add 2001:5c0:9168::1/64 dev ens4f0np0

pkt = Ether(src='04:3f:72:d6:b8:2a',dst='33:33:ff:00:00:02')/IPv6(version=6, src='2001:5c0:9168::2', dst='2001:5c0:9168::1')/ICMPv6ND_NS(tgt='2001:5c0:9168::1')
sendp(pkt, iface='ens4f0np0')

Netperf

https://dl.fedoraproject.org/pub/epel/8/Everything/x86_64/Packages/n/netperf-2.7.0-1.20210803git3bc455b.el8.x86_64.rpm

Netns

ip netns add s1
ip netns add s2
ip netns add s3
ip netns add s4

ip link set ens4f0v0 netns s1
ip link set ens4f0v1 netns s2
ip link set ens4f0v2 netns s3
ip link set ens4f0v3 netns s4

ip netns exec s1 ip addr add 1.1.1.1/24 dev ens4f0v0
ip netns exec s2 ip addr add 1.2.1.1/24 dev ens4f0v1
ip netns exec s3 ip addr add 1.3.1.1/24 dev ens4f0v2
ip netns exec s4 ip addr add 1.4.1.1/24 dev ens4f0v3

ip netns exec s1 ip link set dev ens4f0v0 up
ip netns exec s2 ip link set dev ens4f0v1 up
ip netns exec s3 ip link set dev ens4f0v2 up
ip netns exec s4 ip link set dev ens4f0v3 up

ip netns exec s1 netserver
ip netns exec s2 netserver
ip netns exec s3 netserver
ip netns exec s4 netserver

ip netns exec s1 netperf -H 1.1.1.2 -t TCP_RR > s1.txt &
ip netns exec s2 netperf -H 1.2.1.2 -t TCP_RR > s2.txt &
ip netns exec s3 netperf -H 1.3.1.2 -t TCP_RR > s3.txt &
ip netns exec s4 netperf -H 1.4.1.2 -t TCP_RR > s4.txt &

ip netns exec s1 netperf -H 1.1.1.2 -t TCP_CRR > s1.txt &
ip netns exec s2 netperf -H 1.2.1.2 -t TCP_CRR > s2.txt &
ip netns exec s3 netperf -H 1.3.1.2 -t TCP_CRR > s3.txt &
ip netns exec s4 netperf -H 1.4.1.2 -t TCP_CRR > s4.txt &




ip netns add s1
ip netns add s2
ip netns add s3
ip netns add s4

ip link set ens4f0v0 netns s1
ip link set ens4f0v1 netns s2
ip link set ens4f0v2 netns s3
ip link set ens4f0v3 netns s4

ip netns exec s1 ip addr add 1.1.1.2/24 dev ens4f0v0
ip netns exec s2 ip addr add 1.2.1.2/24 dev ens4f0v1
ip netns exec s3 ip addr add 1.3.1.2/24 dev ens4f0v2
ip netns exec s4 ip addr add 1.4.1.2/24 dev ens4f0v3

ip netns exec s1 ip link set dev ens4f0v0 up
ip netns exec s2 ip link set dev ens4f0v1 up
ip netns exec s3 ip link set dev ens4f0v2 up
ip netns exec s4 ip link set dev ens4f0v3 up

ip netns exec s1 netperf -H 1.1.1.1 -t TCP_RR > s1.txt &
ip netns exec s2 netperf -H 1.2.1.1 -t TCP_RR > s2.txt &
ip netns exec s3 netperf -H 1.3.1.1 -t TCP_RR > s3.txt &
ip netns exec s4 netperf -H 1.4.1.1 -t TCP_RR > s4.txt &

cat s1.txt s2.txt s3.txt s4.txt

ip netns delete s1
ip netns delete s2
ip netns delete s3
ip netns delete s4


ip netns exec s1 ethtool -S ens4f0v0 | grep rx_steer
ip netns exec s2 ethtool -S ens4f0v1 | grep rx_steer
ip netns exec s3 ethtool -S ens4f0v2 | grep rx_steer
ip netns exec s4 ethtool -S ens4f0v3 | grep rx_steer

Grubby

grubby --update=ALL --args='skew_tick=1 tsc=reliable rcupdate.rcu_normal_after_boot=1 nohz=on rcu_nocbs=2-23,32-55 tuned.non_isolcpus=ff000000,ff000003 systemd.cpu_affinity=0,1,56,58,59,57,60,63,61,62,24,25,26,27,28,29,30,31 intel_iommu=on iommu=pt isolcpus=managed_irq,2-23,32-55 tsc=nowatchdog nosoftlockup nmi_watchdog=0 mce=off rcutree.kthread_prio=11 default_hugepagesz=2M intel_pstate=disable'
