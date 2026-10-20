// Read-only synthetic original receipts, never an execution or login authority.
import {
  jobAPIResponse,
  syncJobID,
  exportJobID,
  jobResultFixture,
} from "./job-results-data.mjs";
import {
  operationAPIResponse,
  operationID,
  operationFixture,
} from "./operation-results-data.mjs";
export const otherOperationID = "01800000-0000-7000-8000-000000000059";
export function taskNavigationAPI(url, scenario) {
  const job = scenario.startsWith("navigation-jobs-");
  const list = job ? "/v1/sync/jobs" : "/v1/operations";
  const ids = job ? [syncJobID, exportJobID] : [operationID, otherOperationID];
  if (url.pathname === list) {
    if (scenario.endsWith("list-unavailable"))
      return {
        status: 503,
        error: {
          code: job ? "sync.list_failed" : "operation.list_failed",
          message: "受控任务列表暂不可用",
        },
      };
    return {
      data: job
        ? {
            jobs: ids.map((id) => jobResultFixture("job-partial-sync", id)),
            next_cursor: "",
          }
        : {
            operations: ids.map((id) => ({
              ...operationFixture("operation-partial"),
              operation_id: id,
            })),
            next_cursor: "",
          },
    };
  }
  const id = url.pathname.startsWith(`${list}/`)
    ? url.pathname.slice(list.length + 1)
    : undefined;
  if (id !== undefined) {
    if (!ids.includes(id))
      throw new Error("unexpected navigation receipt target");
    if (scenario.endsWith("detail-denied"))
      return {
        status: 403,
        error: {
          code: job ? "sync.forbidden" : "operation.forbidden",
          message: "受控原详情读取被拒绝",
        },
      };
    if (scenario.endsWith("detail-missing"))
      return {
        status: 404,
        error: {
          code: job ? "sync.not_found" : "operation.not_found",
          message: "受控原回执不存在或不可见",
        },
      };
    return {
      data: job
        ? jobResultFixture("job-partial-sync", id)
        : { ...operationFixture("operation-partial"), operation_id: id },
    };
  }
  if (
    !job &&
    ids.some((value) => url.pathname === `/v1/audit/operations/${value}`)
  )
    return {
      data: {
        operation_id: url.pathname.split("/").at(-1),
        events: [],
        next_cursor: "",
      },
    };
  return job
    ? jobAPIResponse(url, "job-partial-sync")
    : operationAPIResponse(url, "operation-partial");
}
