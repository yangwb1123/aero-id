import * as React from "react";
import { IrisAlert, IrisButton } from "@iris-ui-kit/react";
import type { PageData, JsonRecord } from "../types";
import { RecordTable, type RecordColumn } from "./RecordTable";

export function mergePage<T>(
  current: PageData<T>,
  next: PageData<T>,
  requestedCursor: string,
): PageData<T> {
  if (next.nextCursor === requestedCursor) {
    throw new Error("分页游标没有前进，请刷新后重试");
  }
  return {
    items: [...current.items, ...next.items],
    nextCursor: next.nextCursor,
  };
}

export function PagedRecordTable({
  page,
  loadPage,
  columns,
  empty,
  actionsLabel,
  renderActions,
}: {
  page: PageData<JsonRecord>;
  loadPage(cursor: string): Promise<PageData<JsonRecord>>;
  columns: RecordColumn[];
  empty?: string;
  actionsLabel?: string;
  renderActions?(record: JsonRecord): React.ReactNode;
}): React.ReactElement {
  const [current, setCurrent] = React.useState(page);
  const [loadingMore, setLoadingMore] = React.useState(false);
  const [loadError, setLoadError] = React.useState<Error>();
  const generation = React.useRef(0);

  React.useEffect(() => {
    generation.current += 1;
    setCurrent(page);
    setLoadingMore(false);
    setLoadError(undefined);
  }, [page]);

  const loadMore = async () => {
    const cursor = current.nextCursor;
    if (!cursor || loadingMore) return;
    const activeGeneration = generation.current;
    setLoadingMore(true);
    setLoadError(undefined);
    try {
      const next = await loadPage(cursor);
      if (generation.current !== activeGeneration) return;
      setCurrent(mergePage(current, next, cursor));
    } catch (reason) {
      if (generation.current !== activeGeneration) return;
      setLoadError(
        reason instanceof Error ? reason : new Error("下一页读取失败"),
      );
    } finally {
      if (generation.current === activeGeneration) setLoadingMore(false);
    }
  };

  return (
    <>
      <RecordTable
        records={current.items}
        columns={columns}
        empty={empty}
        actionsLabel={actionsLabel}
        renderActions={renderActions}
      />
      {loadError ? (
        <IrisAlert tone="danger">
          <div className="alert-actions">
            <span>{loadError.message}</span>
            <IrisButton
              size="sm"
              variant="outline"
              onClick={() => void loadMore()}
            >
              重试
            </IrisButton>
          </div>
        </IrisAlert>
      ) : null}
      {current.nextCursor && !loadError ? (
        <div className="pagination-actions">
          <IrisButton
            size="sm"
            variant="outline"
            loading={loadingMore}
            onClick={() => void loadMore()}
          >
            加载更多
          </IrisButton>
        </div>
      ) : null}
    </>
  );
}
