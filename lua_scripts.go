package eonId

// Lua scripts for eon-id Redis operations (worker ID counter, heartbeat)
// All scripts are executed atomically by Redis

// LuaScriptIncrWithReset atomically increments and wraps when exceeding max; returns value in [1, totalWorkerIDs] to avoid out-of-range workerID under concurrency.
// KEYS[1]: counter key
// ARGV[1]: totalWorkerIDs (max worker ID + 1)
// Returns: 1..totalWorkerIDs
const LuaScriptIncrWithReset = `
local total = tonumber(ARGV[1])
local counter = redis.call('INCR', KEYS[1])
if counter > total then
    local next_val = ((counter - 1) % total) + 1
    redis.call('SET', KEYS[1], tostring(next_val))
    return next_val
end
return counter
`

// LuaScriptHeartbeat atomically verifies instance_id and refreshes TTL; uses cjson to parse JSON so values containing quotes do not break regex.
// KEYS[1]: worker key
// ARGV[1]: new worker info JSON
// ARGV[2]: expected instanceID
// ARGV[3]: TTL in seconds
// Returns: 1=success, 0=instanceID mismatch, -1=key not exist, -2=invalid format
const LuaScriptHeartbeat = `
local current = redis.call('GET', KEYS[1])
if not current then
    return -1
end
local ok, t = pcall(cjson.decode, current)
if not ok or not t or type(t.instance_id) ~= 'string' then
    return -2
end
if t.instance_id ~= ARGV[2] then
    return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[3])
return 1
`
