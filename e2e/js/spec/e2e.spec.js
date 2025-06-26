/**
 * SPDX-License-Identifier: BUSL-1.1
 *
 * Copyright (C) 2025 Two Factor Authentication Service, Inc.
 * Licensed under the Business Source License 1.1
 * See LICENSE file for full terms
 */

const serverURLWS = 'ws://localhost:8080';
const serverURLHTTP = 'http://localhost:8080';
jasmine.DEFAULT_TIMEOUT_INTERVAL = 100000;

it('Test Happy Path', async () => {
    return await new Promise(async (resolve, reject) => {
        try {
            const connectionID = crypto.randomUUID();

            // When the test is running, closed connection should fail it.
            // During the test teardown there will close the connection manually and it shouldn't fail the test.
            let awaitingClose = false;
            let rejectWhenClosedTooEarly = function (event) {
                if (awaitingClose) {
                    return;
                }
                reject(`Unexpected close: ${event.code} ${event.reason}`);
            }

            const beConn = await dialBrowserExtension(connectionID, rejectWhenClosedTooEarly);
            const mobileConn = await dialMobile(connectionID, rejectWhenClosedTooEarly);

            await testProxyBothWays(mobileConn, beConn, 'mobile->be', 'be->mobile', 1000);

            // checkCloseCode and resolve the parent promise on the second close event.
            let closed = 0;
            let checkCloseCode = function (expectedCode) {
                return function (event) {
                    if (event.code !== expectedCode) {
                        reject(`Unexpected close code: ${event.code} ${event.reason}`);
                    }
                    closed++;
                    if (closed === 2) {
                        resolve();
                    }
                }
            }

            // We will close one connection manually and the other one should be closed by the server.
            // One closed manually will get status 1005 (no status provided) because we don't provide status in beConn.close().
            // The other one should get 3004 (another party went away) from the server.
            beConn.addEventListener('close', checkCloseCode(1005));
            mobileConn.addEventListener('close', checkCloseCode(3004));

            awaitingClose = true;
            beConn.close();
        } catch (err) {
            reject(err);
        }
    });
});

async function dialBrowserExtension(connectionID, globalReject) {
    return dialWS(`proxy/browser_extension/${connectionID}`, globalReject);
}

async function dialMobile(connectionID, globalReject) {
    return dialWS(`proxy/mobile/${connectionID}`, globalReject);
}

async function dialWS(endpoint, globalReject) {
    return new Promise((resolve, reject) => {
        const url = `${serverURLWS}/${endpoint}`;
        const ws = new WebSocket(url, ["2FAS-Pass"]);
        ws.addEventListener('open', () => {resolve(ws)});
        ws.addEventListener('error', (err) => {
            globalReject(`WebSocket error: ${err}`);
            reject(`WebSocket dial error: ${err}`);
        });
        ws.addEventListener('close', (event) => {
            globalReject(`WebSocket closed: ${event.code} ${event.reason}`);
            reject(`WebSocket closed: ${event.code} ${event.reason}`);
        });
    })
}

async function testProxyBothWays(leftConn, rightConn, leftPayload, rightPayload, iterations) {
    await Promise.all([
        testWriteReceive(leftConn, rightConn, leftPayload, iterations),
        testWriteReceive(rightConn, leftConn, rightPayload, iterations),
    ]);
}

async function testWriteReceive(writeWS, readWS, message, iterations) {
    return new Promise((resolve, reject) => {
        console.log("Will send", iterations, "messages");

        let received = 0;
        readWS.addEventListener('message', (event) => {
            console.log("Received message");
            received++;
            expect(event.data).toEqual(message)
            if (received === iterations) {
                resolve();
            }
        });


        for (let i = 0; i < iterations; i++) {
            console.log("Will send", i, "message");

            writeWS.send(message, (err) => {
                console.log("Error sending", err);
                if (err) reject(err);
            });
        }
    });
}
