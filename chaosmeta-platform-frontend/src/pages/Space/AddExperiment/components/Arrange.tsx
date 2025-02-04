import { arrangeNodeTypeColors, nodeTypes, scaleStepMap } from '@/constants';
import { formatDuration, getIntlLabel, handleTimeTransform } from '@/utils/format';
import {
  DeleteOutlined,
  ExclamationCircleFilled,
  ZoomInOutlined,
  ZoomOutOutlined,
} from '@ant-design/icons';
import {
  SortableContext,
  horizontalListSortingStrategy,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable';
import { Modal, Space } from 'antd';
import React, { useEffect, useState } from 'react';
import { ArrangeContainer, DroppableRow, HandleMove } from '../style';
import DroppableItem from './DroppableItem';
import { useIntl } from '@umijs/max';

interface IProps {
  arrangeList: any[];
  setArrangeList: any;
  timeCount: number;
  setTimeCount: any;
  activeCol: any;
  setActiveCol: any;
  disabled?: boolean;
}
/**
 * 编排区域内容
 * @param props
 * @returns
 */
const Arrange: React.FC<IProps> = (props) => {
  const {
    arrangeList,
    setArrangeList,
    timeCount = 16,
    setTimeCount,
    activeCol,
    setActiveCol,
    disabled = false,
  } = props;
  const listMin = [...Array(timeCount)].map((x, i) => i);
  const [scrollTop, setScrollTop] = useState(0);
  // 当前选中的行
  const [activeRow, setActiveRow] = useState<any>({ state: false });
  const [hoverDelete, setHoverDelete] = useState(false);
  // 当前占比
  const [curProportion, setCurProportion] = useState<number>(100);
  const scaleStep = [33, 66, 100, 150, 200, 300];
  // 统计总时长
  const [totalDuration, setTotalDuration] = useState(0);
  const intl = useIntl();
  /**
   * @description: 处理函数，计算二级列表中所有子项的总时长并更新到总时长上
   */
  const handleTotalSecond = () => {
    // 初始化总时长为0
    let totalSecond: number = 0;
    // 遍历二级列表中的每一个元素
    arrangeList?.forEach((item) => {
      // 遍历该元素的每一个子项
      item?.children?.forEach((el: { duration: string }) => {
        const second = formatDuration(el?.duration)
        // 将子项的秒数累加到总时长上
        totalSecond += second;
      });
    });
    // 更新总时长
    setTotalDuration(totalSecond);
  };

  /**
   * 时间轴渲染
   * @param index
   * @returns
   */
  const renderTimeItem = (index: number) => {
    const secondStep = scaleStepMap[curProportion]?.secondStep;
    const second = index * secondStep;
    const text = handleTimeTransform(second);
    // 时间轴距离间隔固定90px
    return (
      <div key={index} className="time-item" style={{ width: `90px` }}>
        {text}
      </div>
    );
  };

  /**
   * 删除行节点
   */
  const handleDeleteRow = (row: any) => {
    Modal.confirm({
      title: '确认要删除这一行吗？',
      icon: <ExclamationCircleFilled />,
      content: '删除该行，则该行所有配置的节点都将删除，而且不可返回。',
      onOk() {
        setArrangeList((values: any[]) => {
          const newList = values?.filter((item) => item.row !== row);
          return newList;
        });
      },
    });
  };

  /**
   * 编排行节点
   */
  const DroppableContainer = (props: { itemData: any; index: number }) => {
    const { itemData, index } = props;
    // 为第一行或最后行且行内没有数据时，禁用拖动
    const rowDisabled =
      !itemData?.children?.length &&
      (index === 0 || index === arrangeList?.length - 1);
    const params = useSortable({
      id: itemData?.row,
      disabled: disabled || rowDisabled,
      data: {
        ...itemData,
        dragtype: 'row',
        index,
      },
    });
    const { setNodeRef, transform, listeners, isDragging, node } = params;
    return (
      <>
        {/* 行 */}
        <DroppableRow
          ref={setNodeRef}
          $isDragging={isDragging}
          $transform={transform}
          $offsetTop={node.current?.offsetTop}
          $index={index}
          $activeState={activeRow?.row === itemData?.row && activeRow?.state}
          $hoverState={hoverDelete}
        >
          <div className="row" {...listeners}>
            {itemData?.children && (
              <SortableContext
                items={itemData?.children}
                strategy={horizontalListSortingStrategy}
              >
                {/* 行内子元素 */}
                {itemData?.children?.map((el: any, j: number) => {
                  return (
                    <DroppableItem
                      key={j}
                      index={j}
                      item={el}
                      parentId={itemData?.row}
                      activeCol={activeCol}
                      setActiveCol={setActiveCol}
                      curProportion={curProportion}
                      disabled={disabled}
                      setArrangeList={setArrangeList}
                    />
                  );
                })}
              </SortableContext>
            )}
          </div>
          {/* <div className="handle" {...listeners}>
            {index + 1}
          </div> */}
          <div className="moveing"></div>
        </DroppableRow>
        {/* 拖动手柄 */}
        {!isDragging && index !== 0 && index !== arrangeList?.length - 1 && (
          <>
            <HandleMove
              $index={index}
              $activeState={
                activeRow?.row === itemData?.row && activeRow?.state
              }
              $hoverState={hoverDelete}
              $scrollTop={scrollTop}
              className="handle-move"
            >
              {activeRow?.row === itemData?.row && activeRow?.state && (
                <DeleteOutlined
                  className="delete"
                  onClick={() => {
                    handleDeleteRow(itemData?.row);
                  }}
                  onMouseOver={() => {
                    if (!hoverDelete) {
                      setHoverDelete(true);
                    }
                  }}
                  onMouseOut={() => {
                    if (hoverDelete) {
                      setHoverDelete(false);
                    }
                  }}
                />
              )}
              <div
                className="handle"
                {...listeners}
                onClick={() => {
                  if (activeRow?.row === itemData?.row) {
                    setActiveRow({ state: false });
                  } else {
                    setActiveRow({ ...itemData, state: true });
                  }
                }}
              >
                {index}
              </div>
            </HandleMove>
          </>
        )}
      </>
    );
  };

  useEffect(() => {
    // 比例变化时，修改时间间隔的数量，不低于屏幕宽度的秒数，避免出现空白区域，默认1000s
    const doc = document.body;
    const secondStep = scaleStepMap[curProportion]?.secondStep;
    const widthSecond = scaleStepMap[curProportion]?.widthSecond;
    const second = doc.clientWidth / widthSecond;
    setTimeCount(() => {
      const minCount = Math.round(second / secondStep);
      const curCount = Math.round(1000 / secondStep);
      return curCount > minCount ? curCount : minCount;
    });
  }, [curProportion]);

  useEffect(() => {
    handleTotalSecond();
  }, [arrangeList]);

  return (
    <ArrangeContainer $activeColState={activeCol?.uuid}>
      <div
        className="flow"
        onScroll={(event: any) => {
          const curScrollTop = event?.target?.scrollTop || 0;
          setScrollTop(curScrollTop);
        }}
      >
        {/* 顶部时间轴 */}
        <div
          className="time-axis"
          style={{
            minWidth: `${timeCount * 90}px`,
          }}
        >
          {listMin?.map((item, index) => {
            return renderTimeItem(index);
          })}
        </div>
        {/* 编排内容 */}
        <div
          className="center-content"
          style={{
            width: '100%',
            minWidth: `${timeCount * 90}px`,
          }}
        >
          <SortableContext
            items={arrangeList}
            strategy={verticalListSortingStrategy}
            disabled={disabled}
          >
            {arrangeList?.map((item, index) => {
              return (
                <DroppableContainer key={index} index={index} itemData={item} />
              );
            })}
          </SortableContext>
        </div>
      </div>
      <div className="footer">
        <Space style={{ alignItems: 'center' }}>
          <div>
            {intl.formatMessage({id: 'totalDuration'})}：
            <span className="total-time">
              {handleTimeTransform(totalDuration)}
            </span>
          </div>
          <Space className="node-type">
            {nodeTypes?.map((item) => {
              return (
                <Space key={item.label} className="node-item">
                  <div
                    style={{ background: arrangeNodeTypeColors[item.type] }}
                  ></div>
                  {getIntlLabel(item)}
                </Space>
              );
            })}
          </Space>
        </Space>
        <Space>
          <ZoomOutOutlined
            style={{ color: curProportion === 33 ? 'rgba(0,0,0,0.16)' : '' }}
            onClick={() => {
              if (curProportion > 33) {
                setCurProportion(() => {
                  const curIndex = scaleStep?.findIndex(
                    (item) => item === curProportion,
                  );
                  return scaleStep[curIndex - 1];
                });
              }
            }}
          />
          <span>{curProportion}%</span>
          <ZoomInOutlined
            style={{ color: curProportion === 300 ? 'rgba(0,0,0,0.16)' : '' }}
            onClick={() => {
              if (curProportion < 300) {
                setCurProportion(() => {
                  const curIndex = scaleStep?.findIndex(
                    (item) => item === curProportion,
                  );
                  return scaleStep[curIndex + 1];
                });
              }
            }}
          />
        </Space>
      </div>
    </ArrangeContainer>
  );
};

export default Arrange;
