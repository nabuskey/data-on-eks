---
sidebar_position: 4
sidebar_label: JupiterHub wth Dask
---
import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';
import CollapsibleContent from '../../src/components/CollapsibleContent';

# JupiterHub wth Dask

## Introduction

Jupyter is one of the most popular and important open-source software used for data analysis worldwide. Jupyter notebooks allow for data scientists and researchers to leverage an easy to use and flexible user interface to conduct analysis in R, Python, and Julia. Jupyter can also be leveraged to set up a shared environment known as a Jupyterhub where multiple researchers can share notebooks for different analyses, collaborate to solve problems and share data easily with one another. 
This example leverages Crossplane to create a EKS cluster with DaskHub installed from a single YAML file.

<CollapsibleContent header={<h2><span>JupyterHub wth Dask, Cluster Auto Scaler, and Crossplane</span></h2>}>

This option creates a hub EKS cluster with Crossplane and its providers installed. This is the management cluster that is responsible for creating AWS and Kubernetes resources.
Using the management cluster, we will create another EKS cluster with JupyterHub, [Dask](https://www.dask.org/), and Cluster Auto Scaler installed.

![img.png](img/daskhub-diagram.png)

1. Create your management EKS cluster by following steps available in[ this document](https://github.com/awslabs/crossplane-on-eks/tree/main/bootstrap/terraform#how-to-deploy).

2. Once your management cluster is ready, ensure your Crossplane providers are installed and healthy. 
    ```bash
    $ kubectl get Providers
    NAME                  INSTALLED   HEALTHY   PACKAGE                                                         AGE
    provider-aws          True        True      xpkg.upbound.io/crossplane-contrib/provider-aws:v0.39.0         89d
    provider-helm         True        True      xpkg.upbound.io/crossplane-contrib/provider-helm:v0.14.0        6d
    provider-kubernetes   True        True      xpkg.upbound.io/crossplane-contrib/provider-kubernetes:v0.7.0   6d
    ```

3. From the Crossplane on EKS repository, apply necessary compositions. 
    ```bash
    $ kubectl apply -f compositions/aws-provider/vpc-subnets/
    $ kubectl apply -f compositions/aws-provider/eks/
    $ kubectl apply -f compositions/aws-provider/daskhub/
    
    # verify installation was successful
    $ kubectl get xrd
    NAME                                   ESTABLISHED   OFFERED   AGE
    xamazonekss.cluster.awsblueprints.io   True          True      5d23h
    xdaskhubs.awsblueprints.io             True          True      5d23h
    xvpcsubnets.network.awsblueprints.io   True          True      5d23h    
    ```

4. 


</CollapsibleContent>



