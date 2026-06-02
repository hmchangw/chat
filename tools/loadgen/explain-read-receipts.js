// explain-read-receipts.js
// Measures the ListReadReceipts aggregation (room-service/store_mongo.go:924)
// with executionStats, before and after a covering index.
//
//   mongosh "<MONGO_URI>" explain-read-receipts.js
//
// Pick a WORST-CASE input: a large room + an OLD message timestamp, so the
// `lastSeenAt >= since` predicate matches (nearly) every member and the
// $lookup fan-out is maximal. That's the case that hurts under load.

// ---- tune these to your data --------------------------------------------
const DB_NAME   = "chat";                 // room-service's Mongo database
const ROOM_ID   = "<LARGE_ROOM_ID>";      // a room with ~1000 members
const SINCE     = new Date("2000-01-01"); // far past => matches everyone (worst case)
const EXCLUDE   = "<SENDER_ACCOUNT>";     // the message sender's account
const LIMIT     = 1000;                   // h.maxRoomSize
// -------------------------------------------------------------------------

const db = db.getSiblingDB(DB_NAME);
const subs = db.subscriptions;

// Exact pipeline from ListReadReceipts.
const pipeline = [
  { $match: { roomId: ROOM_ID, lastSeenAt: { $gte: SINCE }, "u.account": { $ne: EXCLUDE } } },
  { $lookup: {
      from: "users",
      let: { uid: "$u._id" },
      pipeline: [
        { $match: { $expr: { $eq: ["$_id", "$$uid"] } } },
        { $project: { _id: 1, account: 1, chineseName: 1, engName: 1 } },
      ],
      as: "user",
  }},
  { $unwind: { path: "$user", preserveNullAndEmptyArrays: false } },
  { $replaceWith: "$user" },
  { $limit: LIMIT },
];

function summarize(label) {
  const exp = subs.explain("executionStats").aggregate(pipeline);

  // The $match/FETCH side lives in the $cursor stage (or top-level
  // executionStats depending on server version).
  const es = exp.executionStats ||
             (exp.stages && exp.stages[0] && exp.stages[0].$cursor &&
              exp.stages[0].$cursor.executionStats) || {};

  print(`\n===== ${label} =====`);
  print(`executionTimeMillis : ${es.executionTimeMillis}`);
  print(`nReturned (match)   : ${es.nReturned}`);
  print(`totalKeysExamined   : ${es.totalKeysExamined}`);
  print(`totalDocsExamined   : ${es.totalDocsExamined}`);

  // Walk the winning plan to see if the match used an index and whether a
  // FETCH stage is present (FETCH => index is NOT covering).
  const stages = [];
  let st = es.executionStages;
  while (st) {
    stages.push(`${st.stage}${st.indexName ? "(" + st.indexName + ")" : ""}`);
    st = st.inputStage;
  }
  print(`match plan          : ${stages.reverse().join(" -> ")}`);
  print(`  covered?          : ${stages.indexOf("FETCH") === -1 ? "YES (no FETCH)" : "NO (FETCH present)"}`);

  // $lookup execution stats (per-version; print what we can find).
  if (exp.stages) {
    for (const s of exp.stages) {
      if (s.$lookup) {
        print(`$lookup totalDocsExamined : ${s.totalDocsExamined}`);
        print(`$lookup totalKeysExamined : ${s.totalKeysExamined}`);
        print(`$lookup nReturned         : ${s.nReturned}`);
        if (s.collectionScans !== undefined)
          print(`$lookup collectionScans   : ${s.collectionScans}  <-- must be 0`);
      }
    }
  }
}

// ---- BEFORE: current indexes --------------------------------------------
print("indexes on subscriptions:");
printjson(subs.getIndexes().map(i => i.name));
summarize("BEFORE  (current (roomId,lastSeenAt) index)");

// ---- isolate the inner join: does users._id lookup use IXSCAN? ----------
const sampleUid = subs.findOne({ roomId: ROOM_ID }, { "u._id": 1 });
if (sampleUid) {
  const innerPlan = db.users.find({ _id: sampleUid.u._id })
                            .explain("executionStats").executionStats;
  print(`\ninner users lookup  : ${innerPlan.executionStages.stage} ` +
        `docsExamined=${innerPlan.totalDocsExamined} ` +
        `keysExamined=${innerPlan.totalKeysExamined}  (expect IDHACK/IXSCAN, examined~1)`);
}

// ---- AFTER: add the covering index for the match side -------------------
// Covers roomId(eq) + lastSeenAt(range) + the u.account $ne residual + the
// u._id join key projection => no FETCH on the subscriptions side.
const COVERING = { roomId: 1, lastSeenAt: 1, "u.account": 1, "u._id": 1 };
print("\ncreating covering index { roomId, lastSeenAt, u.account, u._id } ...");
subs.createIndex(COVERING, { name: "rr_covering_tmp" });
summarize("AFTER   (covering index rr_covering_tmp)");

// ---- cleanup: drop the temp index so you don't leave it behind ----------
// Comment out the next line if you want to keep it for a real fix.
subs.dropIndex("rr_covering_tmp");
print("\ndropped rr_covering_tmp (comment out the dropIndex line to keep it).");
