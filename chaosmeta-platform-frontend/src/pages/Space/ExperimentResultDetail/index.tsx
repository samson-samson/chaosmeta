import { PageContainer } from '@ant-design/pro-components';
import { getLocale, history, useIntl, useRequest } from '@umijs/max';
import { Alert, Badge, Card, Progress, Space, Tabs, TabsProps } from 'antd';
import { useEffect, useState } from 'react';
// import ArrangeContent from './ArrangeContent';
// import InfoDrawer from './components/InfoDrawer';
// import ArrangeInfoShow from './ArrangeInfoShow';
import { ExperimentRunPanel } from '@/components/ExperimentRun';
import MetricsPanel from '@/components/ExperimentRun/MetricsPanel';
import RealtimeLogPanel from '@/components/ExperimentRun/RealtimeLogPanel';
import RunStatusBadge from '@/components/ExperimentRun/RunStatusBadge';
import { experimentResultStatus } from '@/constants';
import {
  queryExperimentResultArrangeNodeDetail,
  queryExperimentResultArrangeNodeList,
  queryExperimentResultDetail,
} from '@/services/chaosmeta/ExperimentController';
import {
  arrangeDataOriginTranstion,
  formatDuration,
  getIntlLabel,
} from '@/utils/format';
import ArrangeInfoShow from '../ExperimentDetail/ArrangeInfoShow';
import { Container } from './style';

const AddExperiment = () => {
  // 编排的数据
  const [arrangeList, setArrangeList] = useState([]);
  // 用户权限
  const [tabKey, setTabKey] = useState<'log' | 'index' | string>('log');
  const curExecSecond = '180s';
  // 单个节点详情
  const [curNodeDetail, setCurNodeDetail] = useState<any>({});
  // 结果详情
  const [resultDetail, setResultDetail] = useState<any>({});
  const intl = useIntl();

  /**
   * 获取实验结果详情
   */
  const getResultDetail = useRequest(queryExperimentResultDetail, {
    manual: true,
    formatResult: (res) => res,
    onSuccess: (res) => {
      if (res?.code === 200) {
        setResultDetail(res?.data);
      }
    },
  });

  /**
   * 获取实验结果编排节点list
   */
  const getExperimentArrangeDetail = useRequest(
    queryExperimentResultArrangeNodeList,
    {
      manual: true,
      formatResult: (res) => res,
      onSuccess: (res) => {
        if (res?.code === 200) {
          setArrangeList(
            arrangeDataOriginTranstion(res?.data?.workflow_nodes || [], true),
          );
        }
      },
    },
  );

  /**
   * 获取实验结果单个编排节点
   */
  const getExperimentArrangeNodeDetail = useRequest(
    queryExperimentResultArrangeNodeDetail,
    {
      manual: true,
      formatResult: (res) => res,
      onSuccess: (res) => {
        if (res?.code === 200) {
          const data = res?.data?.workflow_node;
          setCurNodeDetail(data);
        }
      },
    },
  );

  /**
   * 停止/启停后重拉详情（G1：主操作面板 ExperimentRunPanel 的 onStatusChanged 回调，
   * 收到成功动作后刷新当前实例状态，保证顶部按钮与状态徽标同步）
   */
  const refreshAfterAction = () => {
    getResultDetail?.run({
      uuid: history?.location?.query?.resultId as string,
    });
  };

  const headerExtra = () => {
    // v3.1 G1：启停操作的统一三态反馈由顶部 ExperimentRunPanel 承载，
    // 页头这里只保留一个与历史一致的"停止"快捷入口，对运行态可点，走同一个面板的语义。
    return <Space>{/* 操作入口移至下方 ExperimentRunPanel */}</Space>;
  };

  const items: TabsProps['items'] = [
    {
      key: 'log',
      label: intl.formatMessage({ id: 'experimentLog' }),
      // D11: real-time streaming log (SSE + poll fallback) replaces the one-shot ShowLog dump.
      children: (
        <RealtimeLogPanel experimentInstanceUUID={resultDetail?.uuid || ''} />
      ),
    },
    // D12: real process-data visualization replaces the commented-out ObservationCharts placeholder.
    {
      key: 'index',
      label: `实验观测指标`,
      children: (
        <MetricsPanel experimentInstanceUUID={resultDetail?.uuid || ''} />
      ),
    },
  ];

  /**
   * 当前状态匹配
   */
  const handleMateStatus: any = () => {
    const temp = experimentResultStatus?.filter(
      (item) => item?.value === resultDetail?.status,
    )[0];
    return temp;
  };

  // 不同状态展示不同文案
  const statusTextUS: any = {
    Succeeded: 'The run is over and the experiment is successful.',
    Failed: 'The run ends and the experiment fails. Reason for failure: ',
    error: 'End of run, experiment error. wrong reason: ',
  };
  // 不同状态展示不同文案
  const statusText: any = {
    Succeeded: '运行结束，实验成功。',
    Failed: '运行结束，实验失败。失败原因：',
    error: '运行结束，实验错误。错误原因：',
  };

  const renderTitle = () => {
    // D10: unified status badge from any backend/CRD/Argo source.
    return (
      <div style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
        {resultDetail?.name}
        <RunStatusBadge status={resultDetail?.status} />
      </div>
    );
  };

  useEffect(() => {
    const { resultId } = history?.location?.query || {};
    if (resultId) {
      getResultDetail?.run({ uuid: resultId as string });
      getExperimentArrangeDetail?.run({ uuid: resultId as string });
    } else {
      setArrangeList(arrangeDataOriginTranstion([], true));
    }
  }, []);

  return (
    <Container>
      <PageContainer
        header={{
          title: renderTitle(),
          onBack: () => {
            history.back();
          },
          extra: headerExtra(),
        }}
      >
        <div className="content">
          {/* v3.1 G1：主操作面板接线——启动/暂停/恢复/停止四按钮 + 统一三态反馈。
              start 用 experiment_uuid（实验uuid），stop/pause/resume 用实例 uuid（见 useExperimentAction §9.2）。
              onStatusChanged 在动作成功后重拉详情，保持按钮与状态同步。 */}
          {resultDetail?.uuid && (
            <Card
              size="small"
              bodyStyle={{ padding: 0 }}
              style={{ marginBottom: 16, border: 'none' }}
            >
              <ExperimentRunPanel
                experimentInstanceUUID={resultDetail.uuid}
                experimentUUID={resultDetail?.experiment_uuid}
                status={resultDetail?.status}
                onStatusChanged={refreshAfterAction}
              />
            </Card>
          )}
          <div className="content-title">
            <div>{intl.formatMessage({ id: 'experimentProgress' })}</div>
            {/* 后端不支持展示进度，只有成功展示进度条，其他情况展示当前状态 */}
            {resultDetail?.status === 'Succeeded' ? (
              <Progress percent={100} size="small" />
            ) : (
              <span>
                <Badge color={handleMateStatus()?.color} />{' '}
                {getIntlLabel(handleMateStatus())}
              </span>
            )}
          </div>
          {resultDetail?.status &&
            resultDetail?.status !== 'Running' &&
            resultDetail?.status !== 'Pending' && (
              <Alert
                message={
                  <>{`${
                    (getLocale() === 'en-US' ? statusTextUS : statusText)[
                      resultDetail?.status
                    ]
                  }${resultDetail?.message || ''}`}</>
                }
                style={{ marginBottom: '16px' }}
                type={handleMateStatus()?.type}
                showIcon
              />
            )}

          {/* 编排信息的展示 */}
          <ArrangeInfoShow
            arrangeList={arrangeList}
            curExecSecond={formatDuration(curExecSecond)}
            isResult
            getExperimentArrangeNodeDetail={getExperimentArrangeNodeDetail}
            setCurNodeDetail={setCurNodeDetail}
          />
          {/* 日志信息 */}
          <div className="log">
            <Tabs
              defaultActiveKey="log"
              activeKey={tabKey}
              items={items}
              onChange={(key: string) => {
                setTabKey(key);
              }}
            />
          </div>
        </div>
      </PageContainer>
    </Container>
  );
};

export default AddExperiment;
