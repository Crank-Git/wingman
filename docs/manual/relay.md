# Prove a local TURN transfer

You need Go, Docker, and a Linux Docker daemon. The fixture requires network namespace capabilities. See [the deployment guide](../deployment.md#reproduce-the-isolated-acceptance-test) for prerequisites.

1. Open a terminal at the repository root.
2. Run the relay scenario:

   ```sh
   bash scripts/namespace-smoke.sh --scenario relay
   ```

   The fixture starts a local signaling service, a TURN service, and two CLI peers. It blocks direct host paths.

3. Check the transfer result:

   ```text
   namespace relay: both peers receive known text; selected pair: local=relay remote=relay
   namespace two-peer: both CLI peers connect, receive text, and exit after EOF; server exits after SIGTERM
   namespace cleanup: exit 0
   namespace scenario: relay passes
   namespace host cleanup: complete
   ```

4. Check that the command exits 0.

   Exit 77 means a prerequisite prevents the scenario. That result does not prove a transfer.

The review completes the actual relay scenario. Both peers receive known text and report relay candidates in the selected pair. A gathered relay candidate alone does not prove that TURN carries text. The fixture removes its children, container, credentials, and temporary resources.

The scenario uses no public TURN, STUN, or DNS service during the transfer. For your own deployment, permit allocated relay UDP ports as well as the TURN control port.
